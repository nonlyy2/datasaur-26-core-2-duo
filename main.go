package main

import (
	"bytes"
	"database/sql"
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
	_ "github.com/lib/pq"
)

// ═══════════════════════════════════════════════════════════
//  СТРУКТУРЫ ДАННЫХ
// ═══════════════════════════════════════════════════════════

// Manager — один менеджер из таблицы managers.csv
type Manager struct {
	Name     string
	Role     string // Специалист | Ведущий специалист | Главный специалист
	Office   string
	Skills   []string // VIP, ENG, KZ
	Workload int
}

// TicketInput — входные данные одного тикета
type TicketInput struct {
	Index      int
	GUID       string
	Gender     string
	Birthdate  string
	Text       string
	Attachment string
	Segment    string // Mass | VIP | Priority
	Country    string
	Oblast     string
	RawCity    string
	Street     string
	House      string
}

// AIResult — результат AI-анализа одного тикета
type AIResult struct {
	Type          string // Жалоба | Смена данных | Консультация | Претензия | Неработоспособность приложения | Мошеннические действия | Спам
	Sentiment     string // Positive | Neutral | Negative | Legal Risk
	Language      string // RU | KZ | ENG
	Priority      string // "1"-"10"
	Summary       string // Краткая выжимка + рекомендация
	NearestOffice string // Офис из knownOffices
	Source        string // Gemini | Fallback
}

// RoutingResult — итог роутинга одного тикета
type RoutingResult struct {
	GUID           string
	City           string
	Segment        string
	AIType         string
	AISentiment    string
	AILanguage     string
	AIPriority     string
	AISummary      string
	ManagerName    string
	ManagerRole    string
	AssignedOffice string
	RoutingReason  string
	AISource       string
}

// ═══════════════════════════════════════════════════════════
//  ГЛОБАЛЬНЫЕ ПЕРЕМЕННЫЕ
// ═══════════════════════════════════════════════════════════

var (
	ManagersMap     = make(map[string][]*Manager)
	OfficesMap      = make(map[string]string) // Офис → Адрес
	RRCounters      = make(map[string]int)
	foreignSplitCtr int
	HQ_CITIES       = []string{"Астана", "Алматы"}
	knownOffices    []string
	db              *sql.DB
)

// ═══════════════════════════════════════════════════════════
//  POSTGRESQL — ИНИЦИАЛИЗАЦИЯ И СХЕМА
// ═══════════════════════════════════════════════════════════

func initDB() {
	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		host := getEnvDefault("DB_HOST", "localhost")
		port := getEnvDefault("DB_PORT", "5432")
		user := getEnvDefault("DB_USER", "postgres")
		password := getEnvDefault("DB_PASSWORD", "postgres")
		dbname := getEnvDefault("DB_NAME", "fire_db")
		connStr = fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			host, port, user, password, dbname,
		)
	}

	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Printf("⚠️ PostgreSQL: не удалось открыть соединение: %v", err)
		db = nil
		return
	}
	if err = db.Ping(); err != nil {
		log.Printf("⚠️ PostgreSQL: нет соединения: %v", err)
		db = nil
		return
	}
	fmt.Println("✅ PostgreSQL: подключено")
	createSchema()
}

func createSchema() {
	schema := `
-- Основные тикеты (входные данные)
CREATE TABLE IF NOT EXISTS tickets (
    guid          VARCHAR(255) PRIMARY KEY,
    gender        VARCHAR(20),
    birthdate     VARCHAR(30),
    description   TEXT,
    attachment    VARCHAR(500),
    segment       VARCHAR(50),
    country       VARCHAR(100),
    oblast        VARCHAR(200),
    city          VARCHAR(200),
    street        VARCHAR(300),
    house         VARCHAR(50),
    created_at    TIMESTAMP DEFAULT NOW()
);

-- AI-анализ каждого тикета (связь 1:1 с tickets)
CREATE TABLE IF NOT EXISTS ai_analysis (
    guid           VARCHAR(255) PRIMARY KEY REFERENCES tickets(guid) ON DELETE CASCADE,
    type           VARCHAR(100),
    sentiment      VARCHAR(50),
    language       VARCHAR(10),
    priority       INTEGER,
    summary        TEXT,
    source         VARCHAR(50),
    nearest_office VARCHAR(100),
    analyzed_at    TIMESTAMP DEFAULT NOW()
);

-- Результат роутинга (связь 1:1 с tickets)
CREATE TABLE IF NOT EXISTS routing_results (
    guid            VARCHAR(255) PRIMARY KEY REFERENCES tickets(guid) ON DELETE CASCADE,
    manager_name    VARCHAR(255),
    manager_role    VARCHAR(100),
    assigned_office VARCHAR(100),
    routing_reason  TEXT,
    routed_at       TIMESTAMP DEFAULT NOW()
);

-- Представление для удобного просмотра всей цепочки
CREATE OR REPLACE VIEW v_full_results AS
SELECT
    t.guid,
    t.city,
    t.segment,
    t.description,
    a.type        AS ai_type,
    a.sentiment   AS ai_sentiment,
    a.language    AS ai_language,
    a.priority    AS ai_priority,
    a.summary     AS ai_summary,
    a.source      AS ai_source,
    r.manager_name,
    r.manager_role,
    r.assigned_office,
    r.routing_reason
FROM tickets t
LEFT JOIN ai_analysis a ON a.guid = t.guid
LEFT JOIN routing_results r ON r.guid = t.guid;
`
	if _, err := db.Exec(schema); err != nil {
		log.Printf("⚠️ Ошибка создания схемы: %v", err)
	} else {
		fmt.Println("✅ PostgreSQL: схема готова (tickets → ai_analysis → routing_results + view)")
	}
}

func saveTicketToDB(t TicketInput) {
	if db == nil {
		return
	}
	_, err := db.Exec(`
		INSERT INTO tickets (guid, gender, birthdate, description, attachment, segment, country, oblast, city, street, house)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (guid) DO NOTHING`,
		t.GUID, t.Gender, t.Birthdate, t.Text, t.Attachment,
		t.Segment, t.Country, t.Oblast, t.RawCity, t.Street, t.House,
	)
	if err != nil {
		log.Printf("⚠️ DB tickets insert %s: %v", t.GUID[:8], err)
	}
}

func saveAIResultToDB(guid string, ai AIResult) {
	if db == nil {
		return
	}
	priority, _ := strconv.Atoi(ai.Priority)
	_, err := db.Exec(`
		INSERT INTO ai_analysis (guid, type, sentiment, language, priority, summary, source, nearest_office)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (guid) DO UPDATE SET
			type=EXCLUDED.type, sentiment=EXCLUDED.sentiment, language=EXCLUDED.language,
			priority=EXCLUDED.priority, summary=EXCLUDED.summary, source=EXCLUDED.source,
			nearest_office=EXCLUDED.nearest_office`,
		guid, ai.Type, ai.Sentiment, ai.Language, priority, ai.Summary, ai.Source, ai.NearestOffice,
	)
	if err != nil {
		log.Printf("⚠️ DB ai_analysis insert %s: %v", guid[:8], err)
	}
}

func saveRoutingToDB(guid string, r RoutingResult) {
	if db == nil {
		return
	}
	_, err := db.Exec(`
		INSERT INTO routing_results (guid, manager_name, manager_role, assigned_office, routing_reason)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (guid) DO UPDATE SET
			manager_name=EXCLUDED.manager_name, manager_role=EXCLUDED.manager_role,
			assigned_office=EXCLUDED.assigned_office, routing_reason=EXCLUDED.routing_reason`,
		guid, r.ManagerName, r.ManagerRole, r.AssignedOffice, r.RoutingReason,
	)
	if err != nil {
		log.Printf("⚠️ DB routing_results insert %s: %v", guid[:8], err)
	}
}

// ═══════════════════════════════════════════════════════════
//  ЗАГРУЗКА CSV ДАННЫХ
// ═══════════════════════════════════════════════════════════

func loadOffices(fp string) {
	file, err := os.Open(fp)
	if err != nil {
		log.Fatalf("❌ Ошибка открытия %s: %v", fp, err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		log.Fatalf("❌ Ошибка чтения %s: %v", fp, err)
	}

	for i, row := range records {
		if i == 0 || len(row) < 2 {
			continue
		}
		city := strings.TrimSpace(strings.TrimPrefix(row[0], "\uFEFF"))
		OfficesMap[city] = strings.TrimSpace(row[1])
		knownOffices = append(knownOffices, city)
	}
	fmt.Printf("✅ Офисов загружено: %d → %v\n", len(OfficesMap), knownOffices)
}

func loadManagers(fp string) {
	file, err := os.Open(fp)
	if err != nil {
		log.Fatalf("❌ Ошибка открытия %s: %v", fp, err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		log.Fatalf("❌ Ошибка чтения %s: %v", fp, err)
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
		name := strings.TrimSpace(strings.TrimPrefix(row[0], "\uFEFF"))
		role := strings.TrimSpace(strings.TrimPrefix(row[1], "\uFEFF"))
		office := strings.TrimSpace(row[2])

		m := &Manager{
			Name:     name,
			Role:     role,
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
	fmt.Printf("✅ Менеджеров загружено: %d по %d офисам\n", total, len(ManagersMap))
}

// ═══════════════════════════════════════════════════════════
//  ВСПОМОГАТЕЛЬНЫЕ ФУНКЦИИ
// ═══════════════════════════════════════════════════════════

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

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
	lower := strings.ToLower(s)
	for _, w := range words {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// isValidOffice — проверяет, что офис существует в нашем списке
func isValidOffice(office string) bool {
	for _, o := range knownOffices {
		if strings.EqualFold(o, strings.TrimSpace(office)) {
			return true
		}
	}
	return false
}

// normalizeOfficeName — возвращает точное название офиса с правильным регистром
func normalizeOfficeName(office string) string {
	office = strings.TrimSpace(office)
	for _, o := range knownOffices {
		if strings.EqualFold(o, office) {
			return o
		}
	}
	// Нечёткое совпадение
	for _, o := range knownOffices {
		if strings.Contains(strings.ToLower(office), strings.ToLower(o)) ||
			strings.Contains(strings.ToLower(o), strings.ToLower(office)) {
			return o
		}
	}
	return ""
}

// ═══════════════════════════════════════════════════════════
//  KEYWORD FALLBACK — если AI недоступен
// ═══════════════════════════════════════════════════════════

func fallbackAnalyze(t TicketInput) AIResult {
	text := t.Text + " " + t.Attachment
	lower := strings.ToLower(text)

	r := AIResult{
		Type:          "Консультация",
		Sentiment:     "Neutral",
		Language:      "RU",
		Priority:      "5",
		Summary:       "Keyword-анализ. Требуется проверка менеджером.",
		NearestOffice: "",
		Source:        "Fallback",
	}

	// ── Определение языка ────────────────────────────────────
	kazWords := []string{"сіз", "өтінемін", "қате", "көмек", "рахмет", "жоқ", "болады",
		"саламатсыздарма", "менде", "бұйрық", "неге", "алуға"}
	engWords := []string{"please", "help", "error", "account", "transfer", "unable",
		"issue", "hello", "dear", "regards", "blocked", "verify", "validation"}

	kazCount, engCount := 0, 0
	for _, w := range kazWords {
		if strings.Contains(lower, w) {
			kazCount++
		}
	}
	for _, w := range engWords {
		if strings.Contains(lower, w) {
			engCount++
		}
	}
	if kazCount >= 2 {
		r.Language = "KZ"
	} else if engCount >= 2 {
		r.Language = "ENG"
	}

	// ── Классификация по ключевым словам ─────────────────────
	switch {
	case containsAny(text, "суд", "прокуратура", "адвокат", "иск", "court", "lawyer",
		"правоохранительные органы", "заявление в", "следственный"):
		r.Type = "Претензия"
		r.Sentiment = "Legal Risk"
		r.Priority = "10"
		r.Summary = "Клиент угрожает обращением в правоохранительные органы или суд. Немедленная эскалация Главному специалисту."

	case containsAny(text, "мошенник", "украли", "взлом", "несанкционированн", "fraud",
		"scam", "мошеннические", "финансовые махинации"):
		r.Type = "Мошеннические действия"
		r.Sentiment = "Negative"
		r.Priority = "9"
		r.Summary = "Подозрение на мошенничество или несанкционированные действия. Срочно в отдел безопасности."

	case containsAny(text, "верните", "возврат", "компенсация", "возместите", "refund",
		"не пришло", "не на моем счету", "списали"):
		r.Type = "Претензия"
		r.Sentiment = "Negative"
		r.Priority = "8"
		r.Summary = "Требование возврата средств. Запросить детали транзакции и подтверждающие документы."

	case containsAny(text, "смена номера", "изменить данные", "паспорт", "реквизиты",
		"смена данных", "изменить номер", "персональные данные", "удалить мои данные"):
		r.Type = "Смена данных"
		r.Priority = "6"
		r.Summary = "Запрос на изменение персональных данных. Запросить документы для верификации."

	case containsAny(text, "не могу войти", "не работает", "вылетает", "зависает",
		"ошибка", "crash", "error", "blocked", "заблокирован", "блокирован",
		"пароль не принимает", "смс не приходит", "код не приходит"):
		r.Type = "Неработоспособность приложения"
		r.Priority = "6"
		r.Summary = "Технический сбой при входе или работе с приложением. Запросить ОС, версию приложения и скриншоты."

	case containsAny(text, "недоволен", "ужасно", "безобразие", "отвратительно", "terrible",
		"мошеннич", "ведете себя как"):
		r.Type = "Жалоба"
		r.Sentiment = "Negative"
		r.Priority = "7"
		r.Summary = "Негативная оценка сервиса. Выслушать, принести извинения, предложить решение."

	case containsAny(text, "акция!", "выиграли", "поздравляем вы", "бесплатно!",
		"специальные цены", "питомник", "тюльпаны", "сварочные", "оборудование",
		"ПЕРВОУРАЛЬСКБАНК", "московская биржа", "safelinks", "enkod.ru"):
		r.Type = "Спам"
		r.Priority = "1"
		r.Sentiment = "Neutral"
		r.Summary = "Входящее сообщение классифицировано как рекламная рассылка."
	}

	return r
}

// ═══════════════════════════════════════════════════════════
//  БАТЧ AI АНАЛИЗ — один запрос на все тикеты
// ═══════════════════════════════════════════════════════════

type ticketForPrompt struct {
	Index   int    `json:"i"`
	Text    string `json:"text"`
	Country string `json:"country,omitempty"`
	Oblast  string `json:"oblast,omitempty"`
	City    string `json:"city,omitempty"`
}

func analyzeBatch(tickets []TicketInput, apiKey string) (map[int]AIResult, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemma-3-27b-it:generateContent?key=" + apiKey

	officesList := strings.Join(knownOffices, " | ")

	var promptTickets []ticketForPrompt
	for _, t := range tickets {
		text := t.Text
		if t.Attachment != "" && t.Text == "" {
			text = "[Вложение: " + t.Attachment + "] — текста нет, проанализируй по имени файла"
		}
		if len(text) > 700 {
			text = text[:700] + "..."
		}
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

	prompt := fmt.Sprintf(`Ты — аналитик клиентских обращений Freedom Broker (Казахстан).
Обработай массив тикетов и верни ТОЛЬКО JSON-массив без маркдауна, пояснений и текста вне массива.

ДОСТУПНЫЕ ОФИСЫ (nearest_office СТРОГО из этого списка):
%s

ПРАВИЛА КЛАССИФИКАЦИИ:
- type (ТОЛЬКО одно из): "Жалоба" | "Смена данных" | "Консультация" | "Претензия" | "Неработоспособность приложения" | "Мошеннические действия" | "Спам"
- sentiment: "Positive" | "Neutral" | "Negative" | "Legal Risk"
  • "Legal Risk" — если клиент угрожает судом, прокуратурой, полицией, правоохранителями
  • "Negative" — если явное недовольство, но без юридических угроз
- language: "RU" | "KZ" | "ENG"
  • KZ — казахский язык (саламатсыздарма, менде, рахмет, қате, бұйрық и т.п.)
  • ENG — английский язык
  • Если язык не определён → "RU"
- priority: целое число 1–10 (10 = максимальная срочность)
  • Legal Risk → 10, Мошеннические действия → 9, VIP-угрозы → 8+, Спам → 1
- summary (для НЕ-спама): 1–2 предложения на русском — суть обращения + конкретная рекомендация менеджеру
- summary (для Спама): только краткое описание, без рекомендации
- nearest_office: определи ближайший офис из СПИСКА ВЫШЕ по полям country/oblast/city
  Учитывай опечатки, транслитерацию, исторические названия, пригороды (Косшы → Астана, Тургень → Алматы)
  Если клиент из другой страны (не Казахстан) или адрес совсем неизвестен → nearest_office: ""

ПРИМЕРЫ ОПРЕДЕЛЕНИЯ ОФИСА:
- Алматинская обл, Тургень → "Алматы"
- Акмолинская, Косшы → "Астана"
- Акмолинская, Кокшетау → "Кокшетау"
- Семипалатинская / ВКО, Усть-Каменогорск → "Усть-Каменогорск"
- г. Алматы, Алматы → "Алматы"
- г. Шымкент → "Шымкент"
- Mangystau obl., Aktau → "Актау"
- Азербайджан, Украина, Россия → ""

ВЕРНИ ТОЛЬКО JSON-МАССИВ (без markdown и любого другого текста):
[{"i":<число>,"type":"...","sentiment":"...","language":"...","priority":<1-10>,"summary":"...","nearest_office":"..."}]

ТИКЕТЫ:
%s`, officesList, string(ticketsJSON))

	body, _ := json.Marshal(map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]interface{}{{"text": prompt}}},
		},
		"generationConfig": map[string]interface{}{
			"temperature":     0.05,
			"maxOutputTokens": 8192,
		},
	})

	fmt.Printf("📤 Отправка батча: %d тикетов → 1 запрос к Gemini AI...\n", len(tickets))

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("HTTP-ошибка: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("rate limit 429 — подождите 60 сек и запустите снова")
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		snippet := string(b)
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		return nil, fmt.Errorf("API HTTP %d: %s", resp.StatusCode, snippet)
	}

	respBytes, _ := io.ReadAll(resp.Body)

	// Парсинг ответа Gemini
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
		return nil, fmt.Errorf("парсинг Gemini ответа: %v", err)
	}
	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("пустой ответ от AI")
	}

	rawText := geminiResp.Candidates[0].Content.Parts[0].Text

	// Очистка markdown-обёртки
	tbt := "```" // три обратных кавычки — нельзя писать внутри raw string
	rawText = strings.ReplaceAll(rawText, tbt+"json", "")
	rawText = strings.ReplaceAll(rawText, tbt, "")
	rawText = strings.TrimSpace(rawText)

	// Поиск JSON массива внутри текста (на случай если LLM добавил пояснения)
	start := strings.Index(rawText, "[")
	end := strings.LastIndex(rawText, "]")
	if start >= 0 && end > start {
		rawText = rawText[start : end+1]
	}

	// Парсинг через interface{} — устойчиво к типу priority (число или строка)
	var rawResults []map[string]interface{}
	if err := json.Unmarshal([]byte(rawText), &rawResults); err != nil {
		return nil, fmt.Errorf("парсинг JSON результатов: %v\nОтвет AI (первые 600 символов): %.600s", err, rawText)
	}

	results := make(map[int]AIResult)
	for _, item := range rawResults {
		// Получаем индекс (ключ "i")
		indexRaw, ok := item["i"]
		if !ok {
			indexRaw, ok = item["index"]
			if !ok {
				continue
			}
		}
		idx := int(indexRaw.(float64))

		// priority — может быть float64 или строка
		priority := "5"
		switch v := item["priority"].(type) {
		case float64:
			priority = strconv.Itoa(int(v))
		case string:
			if v != "" {
				priority = v
			}
		}

		// nearest_office — валидируем и нормализуем
		nearestOffice := ""
		if raw, ok := item["nearest_office"].(string); ok {
			nearestOffice = normalizeOfficeName(raw)
			if raw != "" && nearestOffice == "" {
				fmt.Printf("   ⚠️ AI вернул неизвестный офис '%s' для тикета %d → 50/50\n", raw, idx)
			}
		}

		results[idx] = AIResult{
			Type:          fmt.Sprintf("%v", item["type"]),
			Sentiment:     fmt.Sprintf("%v", item["sentiment"]),
			Language:      fmt.Sprintf("%v", item["language"]),
			Priority:      priority,
			Summary:       fmt.Sprintf("%v", item["summary"]),
			NearestOffice: nearestOffice,
			Source:        "Gemini",
		}
	}

	fmt.Printf("✅ AI батч завершён: получено %d/%d результатов\n", len(results), len(tickets))
	return results, nil
}

// analyzeBatchWithRetry — повторная попытка при ошибке с паузой
func analyzeBatchWithRetry(tickets []TicketInput, apiKey string, maxRetries int) (map[int]AIResult, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		results, err := analyzeBatch(tickets, apiKey)
		if err == nil {
			return results, nil
		}
		lastErr = err
		if strings.Contains(err.Error(), "rate limit") {
			fmt.Printf("⏳ Rate limit. Ожидание 65 секунд (попытка %d/%d)...\n", attempt, maxRetries)
			time.Sleep(65 * time.Second)
		} else {
			fmt.Printf("⚠️ Ошибка AI (попытка %d/%d): %v\n", attempt, maxRetries, err)
			time.Sleep(5 * time.Second)
		}
	}
	return nil, lastErr
}

// ═══════════════════════════════════════════════════════════
//  ЛОГИКА РОУТИНГА — бизнес-правила ТЗ
// ═══════════════════════════════════════════════════════════

// findBestManager — выбирает менеджера из пула по каскаду фильтров + Round Robin
func findBestManager(pool []*Manager, segment string, ai AIResult, officeKey string) *Manager {
	var filtered []*Manager

	for _, m := range pool {
		// ── Фильтр 1: VIP/Priority сегмент ИЛИ высокий приоритет ИЛИ Legal Risk → нужен навык VIP
		if needsVIP(segment) || isHighPriority(ai.Priority) || ai.Sentiment == "Legal Risk" {
			hasVIP := false
			for _, s := range m.Skills {
				if strings.TrimSpace(s) == "VIP" {
					hasVIP = true
					break
				}
			}
			if !hasVIP {
				continue
			}
		}

		// ── Фильтр 2: Смена данных → ТОЛЬКО Главный специалист
		if ai.Type == "Смена данных" {
			if !strings.Contains(m.Role, "Главный") {
				continue
			}
		}

		// ── Фильтр 3: Язык обращения KZ или ENG → менеджер должен владеть языком
		if ai.Language == "ENG" || ai.Language == "KZ" {
			hasLang := false
			for _, s := range m.Skills {
				if strings.TrimSpace(s) == ai.Language {
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

	// ── Балансировка: Least Connections + Round Robin между топ-2
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Workload < filtered[j].Workload
	})
	candidates := filtered
	if len(filtered) > 1 {
		candidates = filtered[:2] // топ-2 наименее загруженных
	}

	winner := candidates[RRCounters[officeKey]%len(candidates)]
	RRCounters[officeKey]++
	winner.Workload++ // увеличиваем нагрузку для следующей итерации
	return winner
}

// routeTicket — полный каскад роутинга согласно ТЗ
// Возвращает: менеджер, назначенный офис, причина роутинга
func routeTicket(t TicketInput, ai AIResult) (*Manager, string, string) {
	targetOffice := ai.NearestOffice
	routingReason := ""

	isKazakhstan := t.Country == "" ||
		strings.Contains(strings.ToLower(t.Country), "казахстан") ||
		strings.EqualFold(t.Country, "kz") ||
		strings.EqualFold(t.Country, "kazakhstan")

	// ── Шаг 1: Определение целевого офиса ────────────────────
	if targetOffice == "" || !isKazakhstan {
		// Клиент из-за рубежа или адрес не определён → 50/50 Астана/Алматы
		if foreignSplitCtr%2 == 0 {
			targetOffice = "Астана"
		} else {
			targetOffice = "Алматы"
		}
		foreignSplitCtr++

		if !isKazakhstan {
			routingReason = fmt.Sprintf("Иностранный клиент (%s) → 50/50 split → %s", t.Country, targetOffice)
		} else {
			routingReason = fmt.Sprintf("Адрес не определён → 50/50 split → %s", targetOffice)
		}
		fmt.Printf("   🌍 '%s' (%s) → %s (50/50)\n", t.RawCity, t.Country, targetOffice)
	} else {
		routingReason = fmt.Sprintf("Ближайший офис по адресу (%s, %s) → %s", t.RawCity, t.Oblast, targetOffice)
		fmt.Printf("   📍 AI-геолокация: '%s' → офис '%s'\n", t.RawCity, targetOffice)
	}

	// ── Шаг 2: Поиск менеджера в целевом офисе ───────────────
	if pool, ok := ManagersMap[targetOffice]; ok {
		if winner := findBestManager(pool, t.Segment, ai, targetOffice); winner != nil {
			routingReason += fmt.Sprintf(" | Назначен: %s (%s)", winner.Name, winner.Role)
			return winner, targetOffice, routingReason
		}
		// Нет подходящего → эскалация
		noMatchReason := buildNoMatchReason(t.Segment, ai)
		fmt.Printf("   🔼 В '%s' нет подходящего менеджера (%s) → эскалация в ГО\n", targetOffice, noMatchReason)
		routingReason += fmt.Sprintf(" | Нет подходящего (%s) → эскалация в ГО", noMatchReason)
	} else {
		routingReason += fmt.Sprintf(" | Офис '%s' не найден в базе → эскалация в ГО", targetOffice)
	}

	// ── Шаг 3: Эскалация в ГО (Астана или Алматы) ────────────
	for _, hq := range HQ_CITIES {
		if hq == targetOffice {
			continue // Не эскалируем в тот же офис
		}
		if pool, ok := ManagersMap[hq]; ok {
			if winner := findBestManager(pool, t.Segment, ai, hq); winner != nil {
				fmt.Printf("   🔼 Эскалировано в ГО → %s (%s)\n", hq, winner.Name)
				routingReason += fmt.Sprintf(" → ГО %s: назначен %s (%s)", hq, winner.Name, winner.Role)
				return winner, hq, routingReason
			}
		}
	}

	// ── Шаг 4: Менеджер не найден ────────────────────────────
	fmt.Printf("   ❌ Менеджер не найден ни в одном офисе\n")
	routingReason += " | ❌ Менеджер не найден"
	return nil, "—", routingReason
}

// buildNoMatchReason — формирует читаемую причину отсутствия подходящего менеджера
func buildNoMatchReason(segment string, ai AIResult) string {
	var reasons []string
	if needsVIP(segment) || isHighPriority(ai.Priority) || ai.Sentiment == "Legal Risk" {
		reasons = append(reasons, "нужен VIP")
	}
	if ai.Type == "Смена данных" {
		reasons = append(reasons, "нужен Главный специалист")
	}
	if ai.Language == "ENG" || ai.Language == "KZ" {
		reasons = append(reasons, "нужен "+ai.Language)
	}
	if len(reasons) == 0 {
		return "все менеджеры перегружены"
	}
	return strings.Join(reasons, ", ")
}

// ═══════════════════════════════════════════════════════════
//  ОСНОВНАЯ ОБРАБОТКА ТИКЕТОВ
// ═══════════════════════════════════════════════════════════

func processAllTickets(fp, apiKey string) {
	file, err := os.Open(fp)
	if err != nil {
		log.Fatalf("❌ Не удалось открыть %s: %v", fp, err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		log.Fatalf("❌ Ошибка чтения tickets: %v", err)
	}

	// ── Читаем уже обработанные GUIDы (инкрементальная обработка) ──
	processedGUIDs := make(map[string]bool)
	needHeader := true
	outPath := "data/results.csv"

	if existing, err := os.Open(outPath); err == nil {
		rows, _ := csv.NewReader(existing).ReadAll()
		existing.Close()
		if len(rows) > 1 {
			needHeader = false
			for _, row := range rows[1:] {
				if len(row) > 0 {
					processedGUIDs[strings.TrimSpace(row[0])] = true
				}
			}
			fmt.Printf("📂 Уже обработано: %d тикетов, обработаем только новые\n", len(processedGUIDs))
		}
	}

	// ── Собираем необработанные тикеты ───────────────────────────
	var tickets []TicketInput
	for i, row := range records {
		if i == 0 || len(row) < 9 {
			continue
		}
		guid := strings.TrimSpace(strings.TrimPrefix(row[0], "\uFEFF"))
		if processedGUIDs[guid] {
			continue
		}
		text := strings.TrimSpace(row[3])
		attach := strings.TrimSpace(row[4])
		if text == "" && attach == "" {
			fmt.Printf("⚠️ Пропускаем GUID %s: нет текста и вложения\n", guid[:min(8, len(guid))])
			continue
		}

		house := ""
		if len(row) > 10 {
			house = strings.TrimSpace(row[10])
		}

		tickets = append(tickets, TicketInput{
			Index:      len(tickets),
			GUID:       guid,
			Gender:     strings.TrimSpace(row[1]),
			Birthdate:  strings.TrimSpace(row[2]),
			Text:       text,
			Attachment: attach,
			Segment:    strings.TrimSpace(row[5]),
			Country:    strings.TrimSpace(row[6]),
			Oblast:     strings.TrimSpace(row[7]),
			RawCity:    strings.TrimSpace(row[8]),
			Street:     strings.TrimSpace(row[9]),
			House:      house,
		})
	}

	if len(tickets) == 0 {
		fmt.Println("✅ Все тикеты уже обработаны. Нечего делать.")
		return
	}
	fmt.Printf("\n🚀 Новых тикетов для обработки: %d\n", len(tickets))

	// ── Открываем выходной файл ───────────────────────────────────
	os.MkdirAll("data", 0755)
	outFile, err := os.OpenFile(outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("❌ Не удалось открыть results.csv: %v", err)
	}
	defer outFile.Close()

	writer := csv.NewWriter(outFile)
	defer writer.Flush()

	// ── Заголовок CSV — СОВПАДАЕТ с ожиданиями app.py ────────────
	if needHeader {
		writer.Write([]string{
			"GUID",
			"Город_оригинал",
			"Сегмент",
			"AI_Тип",
			"AI_Тональность",
			"AI_Язык",
			"AI_Приоритет",
			"AI_Summary",
			"Назначенный_Менеджер",
			"Должность",
			"Офис_назначения",
			"Причина_роутинга",
			"AI_Источник",
		})
		writer.Flush()
	}

	// ── AI АНАЛИЗ (батч-запрос) ───────────────────────────────────
	aiResults, batchErr := analyzeBatchWithRetry(tickets, apiKey, 3)

	if batchErr != nil {
		fmt.Printf("⚠️ AI батч полностью упал: %v\n🔄 Keyword Fallback для всех тикетов\n", batchErr)
		aiResults = make(map[int]AIResult)
		for _, t := range tickets {
			aiResults[t.Index] = fallbackAnalyze(t)
		}
	} else {
		// Fallback для тикетов, которые AI пропустил
		for _, t := range tickets {
			if _, ok := aiResults[t.Index]; !ok {
				fmt.Printf("   ⚠️ AI пропустил тикет %d (GUID %s) → Keyword Fallback\n",
					t.Index, t.GUID[:min(8, len(t.GUID))])
				fb := fallbackAnalyze(t)
				aiResults[t.Index] = fb
			}
		}
	}

	// ── Бизнес-правило: VIP/Priority → принудительный приоритет 10 ──
	for _, t := range tickets {
		if needsVIP(t.Segment) {
			if r, ok := aiResults[t.Index]; ok && r.Priority != "10" {
				fmt.Printf("   👑 %s | Сегмент %s → приоритет 10 (было %s)\n",
					t.GUID[:min(8, len(t.GUID))], t.Segment, r.Priority)
				r.Priority = "10"
				aiResults[t.Index] = r
			}
		}
	}

	// ── РОУТИНГ + ЗАПИСЬ ──────────────────────────────────────────
	fmt.Println("\n📋 Роутинг тикетов...")
	fmt.Println(strings.Repeat("─", 70))

	var allResults []RoutingResult

	for _, t := range tickets {
		ai := aiResults[t.Index]
		shortGUID := t.GUID
		if len(t.GUID) > 8 {
			shortGUID = t.GUID[:8]
		}

		fmt.Printf("\n[%d/%d] %s | Город: %s | Сегмент: %s | Тип: %s | Приоритет: %s | AI-офис: '%s'\n",
			t.Index+1, len(tickets), shortGUID, t.RawCity, t.Segment, ai.Type, ai.Priority, ai.NearestOffice)

		// Сохраняем тикет в PostgreSQL
		saveTicketToDB(t)
		saveAIResultToDB(t.GUID, ai)

		var routingResult RoutingResult

		// ── СПАМ: сохраняем для аналитики, менеджер не назначается ──
		if ai.Type == "Спам" {
			fmt.Printf("   🚫 Спам — менеджер не назначается\n")
			routingResult = RoutingResult{
				GUID:           t.GUID,
				City:           t.RawCity,
				Segment:        t.Segment,
				AIType:         ai.Type,
				AISentiment:    ai.Sentiment,
				AILanguage:     ai.Language,
				AIPriority:     ai.Priority,
				AISummary:      ai.Summary,
				ManagerName:    "—",
				ManagerRole:    "—",
				AssignedOffice: "—",
				RoutingReason:  "Спам — назначение не требуется",
				AISource:       ai.Source,
			}
		} else {
			// Роутинг
			winner, assignedOffice, reason := routeTicket(t, ai)
			managerName, managerRole := "Не найден", "—"
			if winner != nil {
				managerName = winner.Name
				managerRole = winner.Role
				fmt.Printf("   🎯 %s (%s) → офис %s\n", managerName, managerRole, assignedOffice)
			} else {
				fmt.Printf("   ❌ Менеджер не найден\n")
			}

			routingResult = RoutingResult{
				GUID:           t.GUID,
				City:           t.RawCity,
				Segment:        t.Segment,
				AIType:         ai.Type,
				AISentiment:    ai.Sentiment,
				AILanguage:     ai.Language,
				AIPriority:     ai.Priority,
				AISummary:      ai.Summary,
				ManagerName:    managerName,
				ManagerRole:    managerRole,
				AssignedOffice: assignedOffice,
				RoutingReason:  reason,
				AISource:       ai.Source,
			}
		}

		allResults = append(allResults, routingResult)
		saveRoutingToDB(t.GUID, routingResult)

		// Запись в CSV
		writer.Write([]string{
			routingResult.GUID,
			routingResult.City,
			routingResult.Segment,
			routingResult.AIType,
			routingResult.AISentiment,
			routingResult.AILanguage,
			routingResult.AIPriority,
			routingResult.AISummary,
			routingResult.ManagerName,
			routingResult.ManagerRole,
			routingResult.AssignedOffice,
			routingResult.RoutingReason,
			routingResult.AISource,
		})
		writer.Flush()
	}

	// ── Итоговая статистика ───────────────────────────────────────
	printSummary(allResults)
	fmt.Printf("\n✅ Готово! Обработано %d тикетов → %s\n", len(tickets), outPath)
}

// ═══════════════════════════════════════════════════════════
//  ИТОГОВАЯ СТАТИСТИКА
// ═══════════════════════════════════════════════════════════

func printSummary(results []RoutingResult) {
	fmt.Println("\n" + strings.Repeat("═", 70))
	fmt.Println("📊 ИТОГОВАЯ СТАТИСТИКА")
	fmt.Println(strings.Repeat("═", 70))

	typeCounts := make(map[string]int)
	sentimentCounts := make(map[string]int)
	officeCounts := make(map[string]int)
	sourceCounts := make(map[string]int)
	noManager := 0
	spam := 0
	escalated := 0

	for _, r := range results {
		typeCounts[r.AIType]++
		sentimentCounts[r.AISentiment]++
		officeCounts[r.AssignedOffice]++
		sourceCounts[r.AISource]++
		if r.ManagerName == "Не найден" {
			noManager++
		}
		if r.AIType == "Спам" {
			spam++
		}
		if strings.Contains(r.RoutingReason, "ГО") {
			escalated++
		}
	}

	fmt.Printf("  Всего обработано: %d\n", len(results))
	fmt.Printf("  Спам:             %d\n", spam)
	fmt.Printf("  Эскалировано ГО:  %d\n", escalated)
	fmt.Printf("  Без менеджера:    %d\n", noManager)

	fmt.Println("\n  Типы обращений:")
	for t, c := range typeCounts {
		fmt.Printf("    %-40s %d\n", t, c)
	}

	fmt.Println("\n  Тональность:")
	for s, c := range sentimentCounts {
		fmt.Printf("    %-20s %d\n", s, c)
	}

	fmt.Println("\n  Офисы назначения:")
	for o, c := range officeCounts {
		fmt.Printf("    %-30s %d\n", o, c)
	}

	fmt.Println("\n  AI источник:")
	for src, c := range sourceCounts {
		fmt.Printf("    %-15s %d\n", src, c)
	}
}

// ═══════════════════════════════════════════════════════════
//  ВСПОМОГАТЕЛЬНЫЕ
// ═══════════════════════════════════════════════════════════

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ═══════════════════════════════════════════════════════════
//  MAIN
// ═══════════════════════════════════════════════════════════

func main() {
	// Загрузка .env
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env не найден, используются переменные окружения")
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("❌ GEMINI_API_KEY не установлен! Добавьте в .env или переменные окружения.")
	}

	fmt.Println("🔥 FIRE — Freedom Intelligent Routing Engine v6.0")
	fmt.Println("   ✅ Батч AI-анализ: 1 запрос на все тикеты")
	fmt.Println("   ✅ AI-геолокация: LLM определяет офис (опечатки, транслитерация)")
	fmt.Println("   ✅ Каскад фильтров: VIP → Смена данных → Язык → Round Robin")
	fmt.Println("   ✅ Спам: аналитика без назначения")
	fmt.Println("   ✅ Иностранные клиенты: 50/50 Астана/Алматы")
	fmt.Println("   ✅ PostgreSQL: tickets → ai_analysis → routing_results")
	fmt.Println("   ✅ CSV: колонки совместимы с app.py")
	fmt.Println()

	// Определяем путь к файлам
	ticketsPath := findFile("data/tickets.csv", "tickets.csv")
	officesPath := findFile("data/business_units.csv", "business_units.csv")
	managersPath := findFile("data/managers.csv", "managers.csv")

	// Загружаем данные
	loadOffices(officesPath)
	loadManagers(managersPath)

	// Подключаемся к PostgreSQL (опционально, не блокирует работу)
	initDB()

	// Диагностика VIP-покрытия
	fmt.Println("\n--- VIP-покрытие по офисам ---")
	for _, city := range knownOffices {
		mgrs := ManagersMap[city]
		vipCount := 0
		for _, m := range mgrs {
			for _, s := range m.Skills {
				if strings.TrimSpace(s) == "VIP" {
					vipCount++
					break
				}
			}
		}
		flag := "✅"
		if vipCount == 0 {
			flag = "⚠️  НЕТ VIP!"
		}
		fmt.Printf("  %s %-20s %d менеджеров, %d с VIP\n", flag, city, len(mgrs), vipCount)
	}
	fmt.Println()

	// Небольшая пауза перед запуском
	time.Sleep(200 * time.Millisecond)

	// Основная обработка
	processAllTickets(ticketsPath, apiKey)

	// Закрываем соединение с БД
	if db != nil {
		db.Close()
	}
}

// findFile — ищет файл в нескольких вариантах пути
func findFile(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Если не найден, возвращаем первый путь (выдаст ошибку при открытии)
	return paths[0]
}
