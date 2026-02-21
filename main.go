package main

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// --- 1. СТРУКТУРЫ ДАННЫХ ---

type Manager struct {
	Name     string
	Role     string
	Office   string
	Skills   []string
	Workload int
}

type AIResult struct {
	Type      string `json:"type"`
	Sentiment string `json:"sentiment"`
	Language  string `json:"language"`
	Priority  string `json:"priority"`
}

// Глобальные переменные (In-Memory БД)
var (
	ManagersMap = make(map[string][]*Manager)
	OfficesMap  = make(map[string]string)
	RRCount     int

	// Главный офис для эскалации (fallback)
	HQ_CITY = "Астана"
)

// --- 2. ЗАГРУЗКА ДАННЫХ ---

func loadOffices(fp string) {
	file, err := os.Open(fp)
	if err != nil {
		log.Fatalf("Ошибка открытия файла офисов: %v", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		log.Fatalf("Ошибка чтения CSV офисов: %v", err)
	}

	for i, row := range records {
		if i == 0 || len(row) < 2 {
			continue
		}
		OfficesMap[strings.TrimSpace(row[0])] = strings.TrimSpace(row[1])
	}
	fmt.Printf("✅ Загружено офисов: %d\n", len(OfficesMap))
}

func loadManagers(fp string) {
	file, err := os.Open(fp)
	if err != nil {
		log.Fatalf("Ошибка открытия файла менеджеров: %v", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		log.Fatalf("Ошибка чтения CSV менеджеров: %v", err)
	}

	for i, row := range records {
		if i == 0 || len(row) < 5 {
			continue
		}

		rawSkills := strings.Split(row[3], ",")
		var cleanSkills []string
		for _, s := range rawSkills {
			cleanSkills = append(cleanSkills, strings.TrimSpace(s))
		}

		workload, _ := strconv.Atoi(strings.TrimSpace(row[4]))
		office := strings.TrimSpace(row[2])

		manager := &Manager{
			Name:     strings.TrimSpace(row[0]),
			Role:     strings.TrimSpace(strings.TrimPrefix(row[1], "\uFEFF")),
			Office:   office,
			Skills:   cleanSkills,
			Workload: workload,
		}

		ManagersMap[office] = append(ManagersMap[office], manager)
	}

	totalManagers := 0
	for _, mgrs := range ManagersMap {
		totalManagers += len(mgrs)
	}
	fmt.Printf("✅ Загружено менеджеров: %d (по %d городам)\n", totalManagers, len(ManagersMap))
}

// --- 3. AI-АНАЛИЗ ---

// 🆕 ФОЛБЭК: если Gemini API упал — анализируем по ключевым словам
func fallbackAnalyze(text string) *AIResult {
	lower := strings.ToLower(text)

	result := &AIResult{
		Type:      "Консультация",
		Sentiment: "Neutral",
		Language:  "RU",
		Priority:  "Medium",
	}

	// Определяем язык
	kazWords := []string{"сіз", "өтінемін", "қате", "көмек", "банк"}
	engWords := []string{"please", "help", "error", "account", "transfer", "unable"}
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
		result.Language = "KZ"
	} else if engCount >= 2 {
		result.Language = "ENG"
	}

	// Определяем тип и тональность по ключевым словам
	legalWords := []string{"суд", "прокуратура", "жалоба", "адвокат", "иск", "заявление", "court", "lawyer"}
	for _, w := range legalWords {
		if strings.Contains(lower, w) {
			result.Sentiment = "Legal Risk"
			result.Priority = "High"
			result.Type = "Претензия"
			return result
		}
	}

	fraudWords := []string{"мошенник", "обман", "взлом", "украли", "несанкционированн", "fraud", "scam"}
	for _, w := range fraudWords {
		if strings.Contains(lower, w) {
			result.Type = "Мошеннические действия"
			result.Sentiment = "Highly Negative"
			result.Priority = "High"
			return result
		}
	}

	pretensionWords := []string{"верните", "возврат", "компенсация", "возместите", "убытки", "refund"}
	for _, w := range pretensionWords {
		if strings.Contains(lower, w) {
			result.Type = "Претензия"
			result.Sentiment = "Negative"
			result.Priority = "High"
			return result
		}
	}

	complaintWords := []string{"недоволен", "ужасно", "безобразие", "позор", "плохо", "отвратительно", "terrible"}
	for _, w := range complaintWords {
		if strings.Contains(lower, w) {
			result.Type = "Жалоба"
			result.Sentiment = "Negative"
			result.Priority = "Medium"
			return result
		}
	}

	appWords := []string{"приложение", "не работает", "ошибка", "вылетает", "зависает", "баг", "app", "crash", "error"}
	for _, w := range appWords {
		if strings.Contains(lower, w) {
			result.Type = "Неработоспособность приложения"
			result.Priority = "Medium"
			return result
		}
	}

	dataWords := []string{"смените", "изменить", "обновить данные", "паспорт", "реквизиты", "адрес"}
	for _, w := range dataWords {
		if strings.Contains(lower, w) {
			result.Type = "Смена данных"
			return result
		}
	}

	spamWords := []string{"акция!", "скидка", "выиграли", "поздравляем", "бесплатно", "promotion"}
	for _, w := range spamWords {
		if strings.Contains(lower, w) {
			result.Type = "Спам"
			result.Priority = "Low"
			return result
		}
	}

	return result
}

func analyzeTicketText(text string, attachmentName string, apiKey string) (*AIResult, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemma-3-27b-it:generateContent?key=" + apiKey

	prompt := "Ты - классификатор обращений. Верни ТОЛЬКО валидный JSON без маркдауна.\n" +
		"Правила:\n" +
		"- Если просто негатив -> Жалоба\n" +
		"- Если клиент требует возврата средств или материального возмещения -> Претензия\n" +
		"- Если клиент упоминает суд, прокуратуру, адвоката -> sentiment: Legal Risk, priority: High\n" +
		"Структура JSON:\n" +
		"{\n  \"type\": \"Жалоба | Смена данных | Консультация | Претензия | Неработоспособность приложения | Мошеннические действия | Спам\",\n" +
		"  \"sentiment\": \"Positive | Neutral | Negative | Highly Negative | Legal Risk\",\n" +
		"  \"language\": \"RU | KZ | ENG\",\n" +
		"  \"priority\": \"High | Medium | Low\"\n}\n" +
		"Текст: " + text

	parts := []map[string]interface{}{
		{"text": prompt},
	}

	if attachmentName != "" {
		filePath := filepath.Join("data", "attachments", attachmentName)
		imgData, err := os.ReadFile(filePath)
		if err == nil {
			base64Img := base64.StdEncoding.EncodeToString(imgData)
			parts = append(parts, map[string]interface{}{
				"inline_data": map[string]string{
					"mime_type": "image/jpeg",
					"data":      base64Img,
				},
			})
			fmt.Printf(" [ИИ] Прикреплено изображение: %s\n", attachmentName)
		} else {
			fmt.Printf(" [ИИ] ⚠️ Вложение не найдено: %s\n", filePath)
		}
	}

	reqBodyBytes, _ := json.Marshal(map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": parts},
		},
	})

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 🆕 Явная обработка Rate Limit (429)
	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("rate limit (429): квота исчерпана")
	}

	bodyBytes, _ := io.ReadAll(resp.Body)

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(bodyBytes, &geminiResp); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %v", err)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("пустой ответ от ИИ: %s", string(bodyBytes))
	}

	rawJSON := geminiResp.Candidates[0].Content.Parts[0].Text
	rawJSON = strings.TrimPrefix(rawJSON, "```json\n")
	rawJSON = strings.TrimPrefix(rawJSON, "```\n")
	rawJSON = strings.TrimSuffix(rawJSON, "\n```")
	rawJSON = strings.TrimSpace(rawJSON)

	var result AIResult
	if err := json.Unmarshal([]byte(rawJSON), &result); err != nil {
		return nil, fmt.Errorf("ошибка чтения JSON от ИИ: %v\nТекст ИИ: %s", err, rawJSON)
	}

	return &result, nil
}

// --- 4. РОУТИНГ ---

// findBestManager ищет подходящего менеджера в пуле конкретного города
func findBestManager(pool []*Manager, segment string, aiResult *AIResult) *Manager {
	var filtered []*Manager

	for _, m := range pool {
		// VIP / High Priority / Legal Risk → только VIP-навык
		if segment == "VIP" || aiResult.Priority == "High" || aiResult.Sentiment == "Legal Risk" {
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
		if aiResult.Type == "Смена данных" && m.Role != "Главный специалист" {
			continue
		}

		// Языковой фильтр
		if aiResult.Language == "ENG" || aiResult.Language == "KZ" {
			hasLang := false
			for _, s := range m.Skills {
				if s == aiResult.Language {
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

	// Балансировка: Least Connections + Round Robin
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Workload < filtered[j].Workload
	})

	candidates := filtered
	if len(filtered) > 1 {
		candidates = filtered[:2]
	}

	winner := candidates[RRCount%len(candidates)]
	RRCount++
	winner.Workload++

	return winner
}

// 🆕 routeTicket с авто-эскалацией в главный офис
func routeTicket(city string, segment string, aiResult *AIResult) (*Manager, string, error) {
	// 1. Ищем в пуле города клиента
	if pool, ok := ManagersMap[city]; ok {
		if winner := findBestManager(pool, segment, aiResult); winner != nil {
			return winner, city, nil
		}
		// Подходящих нет в локальном офисе → эскалируем
		fmt.Printf(" 🔼 ЭСКАЛАЦИЯ: в %s нет подходящего менеджера, направляем в %s\n", city, HQ_CITY)
	} else {
		fmt.Printf(" 🌍 Город '%s' не в базе, направляем в %s\n", city, HQ_CITY)
	}

	// 2. 🆕 Эскалация в главный офис (Астана)
	if hqPool, ok := ManagersMap[HQ_CITY]; ok {
		if winner := findBestManager(hqPool, segment, aiResult); winner != nil {
			return winner, HQ_CITY + " (ГО)", nil
		}
	}

	// 3. Если даже ГО не справился — ищем в Алматы
	if almatyPool, ok := ManagersMap["Алматы"]; ok {
		if winner := findBestManager(almatyPool, segment, aiResult); winner != nil {
			return winner, "Алматы (ГО)", nil
		}
	}

	return nil, "-", fmt.Errorf("нет подходящего менеджера ни в одном офисе для тикета из %s", city)
}

// --- 5. ОСНОВНАЯ ОБРАБОТКА ---

func processAllTickets(fp string, apiKey string) {
	file, err := os.Open(fp)
	if err != nil {
		log.Fatalf("Ошибка открытия tickets.csv: %v", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		log.Fatalf("Ошибка чтения tickets.csv: %v", err)
	}

	// Считаем уже обработанные строки (для продолжения с места остановки)
	startFrom := 1 // 1 = пропускаем только заголовок по умолчанию
	needHeader := true

	if existing, err := os.Open("data/results.csv"); err == nil {
		r := csv.NewReader(existing)
		rows, _ := r.ReadAll()
		existing.Close()
		if len(rows) > 1 {
			// Уже есть данные — продолжаем с нужной строки
			startFrom = len(rows) // rows включает заголовок
			needHeader = false
			fmt.Printf("📂 Найден results.csv с %d записями, продолжаем с позиции %d\n", len(rows)-1, startFrom)
		}
	}

	// 🔧 ФИКС: правильное открытие outFile с defer outFile.Close()
	outFile, err := os.OpenFile("data/results.csv", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal("Ошибка открытия results.csv:", err)
	}
	defer outFile.Close() // ФИКС: было defer file.Close() — это баг!

	writer := csv.NewWriter(outFile)
	defer writer.Flush()

	// Пишем заголовок только в новый файл
	if needHeader {
		writer.Write([]string{
			"GUID", "Город", "Сегмент", "Текст",
			"AI_Тип", "AI_Тональность", "AI_Язык", "AI_Приоритет",
			"Назначенный_Менеджер", "Должность", "Офис_назначения", "AI_Источник",
		})
	}

	limit := 20
	count := 0
	consecutiveErrors := 0

	fmt.Printf("\n🚀 Начинаем обработку тикетов (пропускаем первые %d строк)...\n", startFrom)

	for i, row := range records {
		// ФИКС: правильный порядок проверок
		if i == 0 {
			continue // пропускаем заголовок CSV
		}
		if i < startFrom {
			continue // пропускаем уже обработанные
		}
		if count >= limit {
			break
		}
		if len(row) < 9 {
			continue
		}

		guid := row[0]
		text := row[3]
		attachment := strings.TrimSpace(row[4])
		segment := row[5]
		city := row[8]

		if strings.TrimSpace(text) == "" && attachment == "" {
			continue
		}

		fmt.Printf("[%d/%d] Тикет: %s | Город: %s | Сегмент: %s\n",
			count+1, limit, guid[:8], city, segment)

		// 🆕 Пробуем AI, при ошибке — фолбэк на ключевые слова
		aiSource := "Gemini"
		aiResult, aiErr := analyzeTicketText(text, attachment, apiKey)
		if aiErr != nil {
			fmt.Printf(" ⚠️ Ошибка ИИ: %v\n", aiErr)
			fmt.Printf(" 🔄 Переключаемся на keyword-фолбэк\n")
			aiResult = fallbackAnalyze(text)
			aiSource = "Fallback"
			consecutiveErrors++

			// Если 3+ ошибки подряд — ждём дольше
			if consecutiveErrors >= 3 {
				fmt.Printf(" ⏳ Много ошибок подряд, пауза 30 сек...\n")
				time.Sleep(30 * time.Second)
				consecutiveErrors = 0
			}
		} else {
			consecutiveErrors = 0
		}

		// Роутинг с авто-эскалацией
		winner, assignedOffice, routeErr := routeTicket(city, segment, aiResult)
		managerName := "Не найден"
		managerRole := "-"

		if routeErr == nil {
			managerName = winner.Name
			managerRole = winner.Role
			fmt.Printf(" ✅ Назначен: %s (%s) → офис: %s\n", managerName, managerRole, assignedOffice)
		} else {
			fmt.Printf(" ❌ %v\n", routeErr)
			assignedOffice = "Не найден"
		}

		writer.Write([]string{
			guid, city, segment, text,
			aiResult.Type, aiResult.Sentiment, aiResult.Language, aiResult.Priority,
			managerName, managerRole, assignedOffice, aiSource,
		})

		count++

		// Пауза для обхода Rate Limit (только если использовали реальный AI)
		if aiSource == "Gemini" {
			time.Sleep(10 * time.Second)
		}
	}

	fmt.Printf("\n✅ Готово! Обработано %d тикетов. Результаты в data/results.csv\n", count)
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("⚠️ Файл .env не найден")
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("❌ GEMINI_API_KEY не установлен!")
	}

	fmt.Println("🚀 FIRE Engine v2.0 запускается...")

	loadOffices("data/business_units.csv")
	loadManagers("data/managers.csv")

	fmt.Println("\n--- Проверка In-Memory БД ---")
	if astanaMgrs, ok := ManagersMap["Астана"]; ok {
		fmt.Printf("В Астане менеджеров: %d\n", len(astanaMgrs))
	}

	processAllTickets("data/tickets.csv", apiKey)
}
