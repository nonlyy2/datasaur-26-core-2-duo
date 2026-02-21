package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// ========== СТРУКТУРЫ ДАННЫХ ==========

type Manager struct {
	Name     string
	Role     string
	Office   string
	Skills   []string
	Workload int
}

// TicketInput — входные данные тикета
type TicketInput struct {
	Index      int
	GUID       string
	Text       string
	Attachment string
	Segment    string
	Country    string
	Oblast     string
	RawCity    string
}

// AIResult — результат анализа одного тикета
type AIResult struct {
	Type          string // Жалоба, Консультация, Претензия и т.д.
	Sentiment     string // Positive, Neutral, Negative, Legal Risk
	Language      string // RU, KZ, ENG
	Priority      string // "1"-"10"
	Summary       string // Краткая выжимка + рекомендация
	NearestOffice string // 🆕 LLM сам определяет ближайший офис по адресу
}

// ========== ГЛОБАЛЬНЫЕ ПЕРЕМЕННЫЕ ==========

var (
	ManagersMap     = make(map[string][]*Manager)
	OfficesMap      = make(map[string]string) // Офис → Адрес
	RRCounters      = make(map[string]int)
	foreignSplitCtr int
	HQ_CITIES       = []string{"Астана", "Алматы"}
)

// knownOffices — список офисов для промпта (заполняется после loadOffices)
var knownOffices []string

// ========== ЗАГРУЗКА ДАННЫХ ==========

func loadOffices(fp string) {
	file, err := os.Open(fp)
	if err != nil {
		log.Fatalf("Ошибка открытия офисов: %v", err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		log.Fatalf("Ошибка чтения CSV офисов: %v", err)
	}

	for i, row := range records {
		if i == 0 || len(row) < 2 {
			continue
		}
		city := strings.TrimSpace(strings.TrimPrefix(row[0], "\uFEFF"))
		OfficesMap[city] = strings.TrimSpace(row[1])
		knownOffices = append(knownOffices, city)
	}
	fmt.Printf("✅ Офисов: %d → %v\n", len(OfficesMap), knownOffices)
}

func loadManagers(fp string) {
	file, err := os.Open(fp)
	if err != nil {
		log.Fatalf("Ошибка открытия менеджеров: %v", err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		log.Fatalf("Ошибка чтения CSV менеджеров: %v", err)
	}

	for i, row := range records {
		if i == 0 || len(row) < 5 {
			continue
		}
		rawSkills := strings.Split(row[3], ",")
		var skills []string
		for _, s := range rawSkills {
			skills = append(skills, strings.TrimSpace(s))
		}
		workload, _ := strconv.Atoi(strings.TrimSpace(row[4]))
		office := strings.TrimSpace(row[2])
		m := &Manager{
			Name:     strings.TrimSpace(strings.TrimPrefix(row[0], "\uFEFF")),
			Role:     strings.TrimSpace(strings.TrimPrefix(row[1], "\uFEFF")),
			Office:   office,
			Skills:   skills,
			Workload: workload,
		}
		ManagersMap[office] = append(ManagersMap[office], m)
	}
	total := 0
	for _, v := range ManagersMap {
		total += len(v)
	}
	fmt.Printf("✅ Менеджеров: %d по %d офисам\n", total, len(ManagersMap))
}

// ========== ВСПОМОГАТЕЛЬНЫЕ ФУНКЦИИ ==========

func isHighPriority(priority string) bool {
	p, err := strconv.Atoi(strings.TrimSpace(priority))
	if err != nil {
		return strings.EqualFold(priority, "high")
	}
	return p >= 7
}

func needsVIP(segment string) bool {
	s := strings.TrimSpace(segment)
	return s == "VIP" || s == "Priority"
}

func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// isValidOffice — проверяет, что LLM вернул реальный офис из нашего списка
func isValidOffice(office string) bool {
	for _, o := range knownOffices {
		if strings.EqualFold(o, strings.TrimSpace(office)) {
			return true
		}
	}
	return false
}

// ========== KEYWORD FALLBACK ==========

func fallbackAnalyze(t TicketInput) AIResult {
	lower := strings.ToLower(t.Text)
	r := AIResult{
		Type:          "Консультация",
		Sentiment:     "Neutral",
		Language:      "RU",
		Priority:      "5",
		Summary:       "Keyword-анализ. Требуется проверка менеджером.",
		NearestOffice: "", // При fallback гео — будет 50/50
	}

	// Язык
	kazCount, engCount := 0, 0
	for _, w := range []string{"сіз", "өтінемін", "қате", "көмек", "рахмет", "жоқ", "болады"} {
		if strings.Contains(lower, w) {
			kazCount++
		}
	}
	for _, w := range []string{"please", "help", "error", "account", "transfer", "unable", "issue"} {
		if strings.Contains(lower, w) {
			engCount++
		}
	}
	if kazCount >= 2 {
		r.Language = "KZ"
	} else if engCount >= 2 {
		r.Language = "ENG"
	}

	// Тип + приоритет
	switch {
	case containsAny(lower, "суд", "прокуратура", "адвокат", "иск", "court", "lawyer"):
		r.Type, r.Sentiment, r.Priority = "Претензия", "Legal Risk", "10"
		r.Summary = "Клиент угрожает судом. Немедленная эскалация Главному специалисту."
	case containsAny(lower, "мошенник", "украли", "взлом", "несанкционированн", "fraud", "scam"):
		r.Type, r.Sentiment, r.Priority = "Мошеннические действия", "Negative", "9"
		r.Summary = "Подозрение на мошенничество. Срочно в отдел безопасности."
	case containsAny(lower, "верните", "возврат", "компенсация", "возместите", "refund"):
		r.Type, r.Sentiment, r.Priority = "Претензия", "Negative", "8"
		r.Summary = "Требование возврата средств. Запросить детали транзакции."
	case containsAny(lower, "недоволен", "ужасно", "безобразие", "отвратительно", "terrible"):
		r.Type, r.Sentiment, r.Priority = "Жалоба", "Negative", "6"
		r.Summary = "Негативная оценка сервиса. Выслушать и принести извинения."
	case containsAny(lower, "не работает", "вылетает", "зависает", "ошибка", "crash", "error"):
		r.Type, r.Priority = "Неработоспособность приложения", "6"
		r.Summary = "Технический сбой. Запросить версию ОС и шаги воспроизведения."
	case containsAny(lower, "смена", "изменить данные", "паспорт", "реквизиты"):
		r.Type, r.Priority = "Смена данных", "5"
		r.Summary = "Запрос на изменение данных. Запросить документы."
	case containsAny(lower, "акция!", "выиграли", "поздравляем вы", "бесплатно!"):
		r.Type, r.Priority = "Спам", "1"
		r.Summary = "Входящее сообщение классифицировано как рекламная рассылка."
	default:
		r.Summary = "Клиент обращается за консультацией. Уточнить детали."
	}

	return r
}

// ========== БАТЧ AI АНАЛИЗ ==========

func analyzeBatch(tickets []TicketInput, apiKey string) (map[int]AIResult, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemma-3-27b-it:generateContent?key=" + apiKey

	// Список офисов для промпта — LLM будет выбирать только из этого списка
	officesList := strings.Join(knownOffices, " | ")

	// Формируем компактный JSON-массив тикетов
	// Передаём все адресные поля — LLM сам разберётся с опечатками и нестандартными названиями
	type ticketForPrompt struct {
		Index   int    `json:"i"`
		Text    string `json:"text"`
		Country string `json:"country,omitempty"`
		Oblast  string `json:"oblast,omitempty"`
		City    string `json:"city,omitempty"`
	}

	var promptTickets []ticketForPrompt
	for _, t := range tickets {
		text := t.Text
		if len(text) > 600 {
			text = text[:600] + "..."
		}
		// Экранируем кавычки в тексте
		text = strings.ReplaceAll(text, `"`, `'`)

		promptTickets = append(promptTickets, ticketForPrompt{
			Index:   t.Index,
			Text:    text,
			Country: t.Country,
			Oblast:  t.Oblast,
			City:    t.RawCity,
		})
	}

	ticketsJSON, _ := json.Marshal(promptTickets)

	prompt := fmt.Sprintf(`Ты — аналитик клиентских обращений Freedom Broker (Казахстан). Обработай массив тикетов.

СПИСОК ДОСТУПНЫХ ОФИСОВ (nearest_office — ТОЛЬКО из этого списка):
%s

ПРАВИЛА КЛАССИФИКАЦИИ:
- Просто негатив → type: "Жалоба"
- Требование возврата/компенсации → type: "Претензия"
- Угроза судом/прокуратурой/адвокатом → sentiment: "Legal Risk", priority: 10
- Реклама/рассылка → type: "Спам", priority: 1
- Язык не определён → language: "RU"
- priority: целое число 1-10 (10 = максимальная срочность)
- summary для НЕ-спама: 1-2 предложения на русском — суть + рекомендация менеджеру
- summary для Спама: только краткое описание без рекомендации (менеджер не назначается)
- nearest_office: определи ближайший офис по полям country/oblast/city.
  Учитывай опечатки, транслитерацию, исторические названия, пригороды.
  Если клиент из другой страны или адрес совсем неизвестен → nearest_office: ""

ВЕРНИ ТОЛЬКО JSON МАССИВ, без маркдауна и пояснений:
[
  {
    "i": <число из входных данных>,
    "type": "Жалоба | Смена данных | Консультация | Претензия | Неработоспособность приложения | Мошеннические действия | Спам",
    "sentiment": "Positive | Neutral | Negative | Legal Risk",
    "language": "RU | KZ | ENG",
    "priority": <1-10>,
    "summary": "<текст>",
    "nearest_office": "<название офиса из списка выше или пустая строка>"
  }
]

ТИКЕТЫ:
%s`, officesList, string(ticketsJSON))

	body, _ := json.Marshal(map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]interface{}{{"text": prompt}}},
		},
		"generationConfig": map[string]interface{}{
			"temperature":     0.1,
			"maxOutputTokens": 8192,
		},
	})

	fmt.Printf("📤 Батч: %d тикетов → 1 запрос к AI...\n", len(tickets))

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("HTTP: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("rate limit 429 — подождите 60 сек")
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		snippet := string(b)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return nil, fmt.Errorf("API %d: %s", resp.StatusCode, snippet)
	}

	respBytes, _ := io.ReadAll(resp.Body)

	// Стандартный парсинг ответа Gemini
	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBytes, &geminiResp); err != nil {
		return nil, fmt.Errorf("парсинг ответа Gemini: %v", err)
	}
	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("пустой ответ от AI")
	}

	rawText := geminiResp.Candidates[0].Content.Parts[0].Text

	// Чистим markdown-обёртку
	rawText = strings.TrimPrefix(rawText, "```json\n")
	rawText = strings.TrimPrefix(rawText, "```json")
	rawText = strings.TrimPrefix(rawText, "```\n")
	rawText = strings.TrimSuffix(rawText, "\n```")
	rawText = strings.TrimSuffix(rawText, "```")
	rawText = strings.TrimSpace(rawText)

	// Парсим массив через interface{} — устойчиво к типу priority (число или строка)
	var rawResults []map[string]interface{}
	if err := json.Unmarshal([]byte(rawText), &rawResults); err != nil {
		// Пробуем найти JSON массив внутри текста
		start := strings.Index(rawText, "[")
		end := strings.LastIndex(rawText, "]")
		if start >= 0 && end > start {
			if err2 := json.Unmarshal([]byte(rawText[start:end+1]), &rawResults); err2 != nil {
				return nil, fmt.Errorf("парсинг JSON: %v\nОтвет AI: %.500s", err2, rawText)
			}
		} else {
			return nil, fmt.Errorf("JSON массив не найден: %.500s", rawText)
		}
	}

	results := make(map[int]AIResult)
	for _, item := range rawResults {
		// index — ключ "i"
		indexRaw, ok := item["i"]
		if !ok {
			// fallback на "index" если LLM использовал полное имя
			indexRaw, ok = item["index"]
			if !ok {
				continue
			}
		}
		idx := int(indexRaw.(float64))

		// priority — может быть float64 или string
		priority := "5"
		switch v := item["priority"].(type) {
		case float64:
			priority = strconv.Itoa(int(v))
		case string:
			if v != "" {
				priority = v
			}
		}

		// nearest_office — проверяем валидность
		nearestOffice := ""
		if raw, ok := item["nearest_office"].(string); ok {
			raw = strings.TrimSpace(raw)
			if isValidOffice(raw) {
				nearestOffice = raw
			} else if raw != "" {
				// LLM вернул что-то похожее — пробуем нечёткое совпадение
				for _, o := range knownOffices {
					if strings.Contains(strings.ToLower(raw), strings.ToLower(o)) ||
						strings.Contains(strings.ToLower(o), strings.ToLower(raw)) {
						nearestOffice = o
						break
					}
				}
				if nearestOffice == "" {
					fmt.Printf("   ⚠️ AI вернул неизвестный офис '%s' → 50/50\n", raw)
				}
			}
		}

		results[idx] = AIResult{
			Type:          fmt.Sprintf("%v", item["type"]),
			Sentiment:     fmt.Sprintf("%v", item["sentiment"]),
			Language:      fmt.Sprintf("%v", item["language"]),
			Priority:      priority,
			Summary:       fmt.Sprintf("%v", item["summary"]),
			NearestOffice: nearestOffice,
		}
	}

	fmt.Printf("✅ Батч готов: %d/%d результатов\n", len(results), len(tickets))
	return results, nil
}

// ========== РОУТИНГ ==========

func findBestManager(pool []*Manager, segment string, ai AIResult, city string) *Manager {
	var filtered []*Manager
	for _, m := range pool {
		// VIP/Priority сегмент ИЛИ приоритет >= 7 ИЛИ Legal Risk → нужен VIP навык
		if needsVIP(segment) || isHighPriority(ai.Priority) || ai.Sentiment == "Legal Risk" {
			hasVIP := false
			for _, s := range m.Skills {
				if s == "VIP" {
					hasVIP = true
					break
				}
			}
			if !hasVIP {
				continue
			}
		}
		// Смена данных → только Главный специалист
		if ai.Type == "Смена данных" && m.Role != "Главный специалист" {
			continue
		}
		// Языковой фильтр
		if ai.Language == "ENG" || ai.Language == "KZ" {
			hasLang := false
			for _, s := range m.Skills {
				if s == ai.Language {
					hasLang = true
					break
				}
			}
			if !hasLang {
				continue
			}
		}
		filtered = append(filtered, m)
	}
	if len(filtered) == 0 {
		return nil
	}

	// Least Connections + Round Robin топ-2
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Workload < filtered[j].Workload
	})
	candidates := filtered
	if len(filtered) > 1 {
		candidates = filtered[:2]
	}
	winner := candidates[RRCounters[city]%len(candidates)]
	RRCounters[city]++
	winner.Workload++
	return winner
}

func routeTicket(t TicketInput, ai AIResult) (*Manager, string) {
	// AI уже определил ближайший офис — используем его напрямую
	targetOffice := ai.NearestOffice

	isKazakhstan := t.Country == "" ||
		strings.Contains(strings.ToLower(t.Country), "казахстан") ||
		strings.EqualFold(t.Country, "kz") ||
		strings.EqualFold(t.Country, "kazakhstan")

	if targetOffice == "" || !isKazakhstan {
		// AI не смог определить офис или клиент из-за рубежа → 50/50
		if foreignSplitCtr%2 == 0 {
			targetOffice = "Астана"
		} else {
			targetOffice = "Алматы"
		}
		foreignSplitCtr++
		fmt.Printf("   🌍 '%s' → %s (50/50)\n", t.RawCity, targetOffice)
	} else {
		fmt.Printf("   📍 AI: '%s' → офис '%s'\n", t.RawCity, targetOffice)
	}

	// Шаг 1: Целевой офис
	if pool, ok := ManagersMap[targetOffice]; ok {
		if winner := findBestManager(pool, t.Segment, ai, targetOffice); winner != nil {
			return winner, targetOffice
		}
		fmt.Printf("   🔼 В '%s' нет подходящего → эскалация в ГО\n", targetOffice)
	}

	// Шаг 2: Эскалация в ГО
	for _, hq := range HQ_CITIES {
		if hq == targetOffice {
			continue
		}
		if pool, ok := ManagersMap[hq]; ok {
			if winner := findBestManager(pool, t.Segment, ai, hq); winner != nil {
				fmt.Printf("   🔼 Эскалировано → %s\n", hq)
				return winner, hq
			}
		}
	}

	fmt.Printf("   ❌ Нет менеджера ни в одном офисе\n")
	return nil, "—"
}

// ========== ОСНОВНАЯ ОБРАБОТКА ==========

func processAllTickets(fp, apiKey string) {
	file, err := os.Open(fp)
	if err != nil {
		log.Fatalf("Ошибка tickets.csv: %v", err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		log.Fatalf("Ошибка чтения: %v", err)
	}

	// Читаем уже обработанные GUIDы
	processedGUIDs := make(map[string]bool)
	needHeader := true
	if existing, err := os.Open("data/results.csv"); err == nil {
		rows, _ := csv.NewReader(existing).ReadAll()
		existing.Close()
		if len(rows) > 1 {
			needHeader = false
			for _, row := range rows[1:] {
				if len(row) > 0 {
					processedGUIDs[strings.TrimSpace(row[0])] = true
				}
			}
			fmt.Printf("📂 Уже обработано: %d тикетов\n", len(processedGUIDs))
		}
	}

	// Собираем необработанные тикеты
	var tickets []TicketInput
	for i, row := range records {
		if i == 0 || len(row) < 9 {
			continue
		}
		guid := strings.TrimSpace(row[0])
		if processedGUIDs[guid] {
			continue
		}
		text := strings.TrimSpace(row[3])
		attach := strings.TrimSpace(row[4])
		if text == "" && attach == "" {
			continue
		}
		tickets = append(tickets, TicketInput{
			Index:      len(tickets),
			GUID:       guid,
			Text:       text,
			Attachment: attach,
			Segment:    strings.TrimSpace(row[5]),
			Country:    strings.TrimSpace(row[6]),
			Oblast:     strings.TrimSpace(row[7]),
			RawCity:    strings.TrimSpace(row[8]),
		})
	}

	if len(tickets) == 0 {
		fmt.Println("✅ Все тикеты уже обработаны.")
		return
	}
	fmt.Printf("\n🚀 Необработанных тикетов: %d\n", len(tickets))

	// Открываем выходной файл
	outFile, err := os.OpenFile("data/results.csv", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal("Ошибка results.csv:", err)
	}
	defer outFile.Close()

	writer := csv.NewWriter(outFile)
	defer writer.Flush()

	if needHeader {
		writer.Write([]string{
			"GUID", "Область", "Сегмент", "Текст",
			"Тип", "Тональность", "Язык", "Приоритет", "Рекомендации менеджеру",
			"Назначенный Менеджер", "Должность", "Офис Назначения",
		})
		writer.Flush()
	}

	// ── БАТЧ AI АНАЛИЗ (1 запрос на всё) ─────────────────────────────────────
	aiResults, batchErr := analyzeBatch(tickets, apiKey)

	if batchErr != nil {
		fmt.Printf("⚠️ Батч ошибка: %v\n🔄 Keyword fallback для всех тикетов\n", batchErr)
		aiResults = make(map[int]AIResult)
		for _, t := range tickets {
			aiResults[t.Index] = fallbackAnalyze(t)
		}
	} else {
		// Fallback для тикетов, которые AI пропустил
		for _, t := range tickets {
			if _, ok := aiResults[t.Index]; !ok {
				fmt.Printf("   ⚠️ AI пропустил тикет %d → fallback\n", t.Index)
				aiResults[t.Index] = fallbackAnalyze(t)
			}
		}
	}

	// VIP / Priority сегмент → принудительно приоритет 10
	for _, t := range tickets {
		if needsVIP(t.Segment) {
			if r, ok := aiResults[t.Index]; ok {
				if r.Priority != "10" {
					fmt.Printf("   👑 %s | сегмент %s → принудительный приоритет 10 (было %s)\n",
						t.GUID[:8], t.Segment, r.Priority)
					r.Priority = "10"
					aiResults[t.Index] = r
				}
			}
		}
	}

	// ── РОУТИНГ И ЗАПИСЬ ──────────────────────────────────────────────────────
	fmt.Println("\n📋 Роутинг...")
	for _, t := range tickets {
		ai := aiResults[t.Index]
		short := t.GUID
		if len(t.GUID) > 8 {
			short = t.GUID[:8]
		}
		fmt.Printf("\n[%d] %s | %s | %s | %s | p=%s | AI-офис: '%s'\n",
			t.Index+1, short, t.RawCity, t.Segment, ai.Type, ai.Priority, ai.NearestOffice)

		// ── СПАМ: сохраняем для аналитики, менеджер не назначается ──
		if ai.Type == "Спам" {
			fmt.Printf("   🚫 Спам — без назначения менеджера\n")
			writer.Write([]string{
				t.GUID, t.Oblast, t.Segment,
				t.Text, ai.Type, ai.Sentiment, ai.Language, ai.Priority,
				ai.Summary,
				"—", "—", "—",
			})
			writer.Flush()
			continue
		}

		// Роутинг
		winner, assignedOffice := routeTicket(t, ai)
		managerName, managerRole := "Не найден", "—"
		if winner != nil {
			managerName = winner.Name
			managerRole = winner.Role
			fmt.Printf("   🎯 %s (%s) → %s\n", managerName, managerRole, assignedOffice)
		}

		writer.Write([]string{
			t.GUID, t.Oblast, t.Segment,
			t.Text, ai.Type, ai.Sentiment, ai.Language, ai.Priority,
			ai.Summary,
			managerName, managerRole, assignedOffice,
		})
		writer.Flush()
	}

	fmt.Printf("\n✅ Готово! Обработано %d тикетов → data/results.csv\n", len(tickets))
}

// ========== MAIN ==========

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env не найден")
	}
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("❌ GEMINI_API_KEY не установлен!")
	}

	fmt.Println("🔥 FIRE Engine v5.0")
	fmt.Println("   ✅ AI-geo: LLM сам определяет офис (опечатки, транслитерация, пригороды)")
	fmt.Println("   ✅ Батч-промпт: 1 запрос на все тикеты")
	fmt.Println("   ✅ Спам: аналитика без назначения")
	fmt.Println("   ✅ Priority segment = VIP-обслуживание")
	fmt.Println("   ✅ Priority 1-10 + JSON fix")
	fmt.Println("   ✅ Авто-эскалация + 50/50 split")
	fmt.Println("   ✅ 0 хардкода адресов")

	loadOffices("data/business_units.csv")
	loadManagers("data/managers.csv")

	// Проверка: VIP-покрытие по офисам
	fmt.Println("\n--- VIP-покрытие по офисам ---")
	for _, city := range knownOffices {
		mgrs := ManagersMap[city]
		vip := 0
		for _, m := range mgrs {
			for _, s := range m.Skills {
				if s == "VIP" {
					vip++
					break
				}
			}
		}
		flag := "✅"
		if vip == 0 {
			flag = "⚠️ НЕТ VIP!"
		}
		fmt.Printf("  %s %s: %d менеджеров, %d VIP\n", flag, city, len(mgrs), vip)
	}

	// Небольшая пауза перед запуском
	time.Sleep(300 * time.Millisecond)

	processAllTickets("data/tickets.csv", apiKey)
}
