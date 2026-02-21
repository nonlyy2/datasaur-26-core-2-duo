package main

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	Type          string  // Жалоба | Смена данных | Консультация | Претензия | Неработоспособность приложения | Мошеннические действия | Спам
	Sentiment     string  // Позитивный | Нейтральный | Негативный
	Language      string  // RU | KZ | ENG
	Priority      string  // "1"-"10"
	Summary       string  // Краткая выжимка + рекомендация (на языке обращения)
	NearestOffice string  // Офис из knownOffices (финальный, после геокодирования)
	GeoLat        float64 // Широта клиента (Nominatim)
	GeoLon        float64 // Долгота клиента (Nominatim)
	GeoMethod     string  // "nominatim" | "llm" | "50/50"
	Source        string  // Gemini | Fallback
}

// RoutingResult — итог роутинга одного тикета
type RoutingResult struct {
	GUID           string
	CityOriginal   string // Город_оригинал
	Segment        string
	Type           string
	Sentiment      string
	Language       string
	Priority       string
	Summary        string
	ManagerName    string
	ManagerRole    string
	AssignedOffice string
	RoutingReason  string // Причина_роутинга
	GeoMethod      string // Метод геокодирования
	Source         string // AI_Источник: Gemini | Fallback
	IsEscalated    bool   // Был ли тикет эскалирован в ГО
}

// ═══════════════════════════════════════════════════════════
//  ГЛОБАЛЬНЫЕ ПЕРЕМЕННЫЕ
// ═══════════════════════════════════════════════════════════

// GeoPoint — координаты точки (широта/долгота)
type GeoPoint struct {
	Lat float64
	Lon float64
}

var (
	ManagersMap     = make(map[string][]*Manager)
	RRCounters      = make(map[string]int)
	foreignSplitCtr int
	HQ_CITIES       = []string{"Астана", "Алматы"}
	knownOffices    []string
	db              *sql.DB

	// OfficeCoords — координаты офисов для расчёта реального расстояния
	OfficeCoords = map[string]GeoPoint{
		"Алматы":           {43.2220, 76.8512},
		"Астана":           {51.1801, 71.4598},
		"Шымкент":          {42.3417, 69.5901},
		"Актобе":           {50.2839, 57.1670},
		"Атырау":           {47.1105, 51.9271},
		"Усть-Каменогорск": {49.9490, 82.6285},
		"Актау":            {43.6515, 51.1726},
		"Петропавловск":    {54.8656, 69.1521},
		"Кокшетау":         {53.2849, 69.3966},
		"Павлодар":         {52.2873, 76.9674},
		"Тараз":            {42.9000, 71.3667},
		"Семей":            {50.4111, 80.2275},
		"Кызылорда":        {44.8488, 65.5091},
		"Уральск":          {51.2333, 51.3667},
		"Костанай":         {53.2141, 63.6324},
	}
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
    geo_lat        DOUBLE PRECISION,
    geo_lon        DOUBLE PRECISION,
    geo_method     VARCHAR(20),
    analyzed_at    TIMESTAMP DEFAULT NOW()
);

-- Результат роутинга (связь 1:1 с tickets)
CREATE TABLE IF NOT EXISTS routing_results (
    guid            VARCHAR(255) PRIMARY KEY REFERENCES tickets(guid) ON DELETE CASCADE,
    manager_name    VARCHAR(255),
    manager_role    VARCHAR(100),
    assigned_office VARCHAR(100),
    is_escalated    BOOLEAN DEFAULT FALSE,
    routing_reason  TEXT,
    city_original   VARCHAR(200),
    routed_at       TIMESTAMP DEFAULT NOW()
);

-- Миграция: добавляем колонки если таблица уже существовала без них
ALTER TABLE routing_results ADD COLUMN IF NOT EXISTS is_escalated   BOOLEAN DEFAULT FALSE;
ALTER TABLE routing_results ADD COLUMN IF NOT EXISTS routing_reason TEXT;
ALTER TABLE routing_results ADD COLUMN IF NOT EXISTS city_original  VARCHAR(200);

-- Представление для удобного просмотра всей цепочки
CREATE OR REPLACE VIEW v_full_results AS
SELECT
    t.guid,
    t.city,
    r.city_original,
    t.segment,
    t.description,
    a.type        AS ai_type,
    a.sentiment   AS ai_sentiment,
    a.language    AS ai_language,
    a.priority    AS ai_priority,
    a.summary     AS ai_summary,
    a.source      AS ai_source,
    a.geo_method  AS geo_method,
    r.manager_name,
    r.manager_role,
    r.assigned_office,
    r.is_escalated,
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
		log.Printf("⚠️ DB tickets insert %s: %v", t.GUID[:min(8, len(t.GUID))], err)
	}
}

func saveAIResultToDB(guid string, ai AIResult) {
	if db == nil {
		return
	}
	priority, _ := strconv.Atoi(ai.Priority)
	var lat, lon any
	if ai.GeoLat != 0 || ai.GeoLon != 0 {
		lat, lon = ai.GeoLat, ai.GeoLon
	}
	_, err := db.Exec(`
		INSERT INTO ai_analysis (guid, type, sentiment, language, priority, summary, source, nearest_office, geo_lat, geo_lon, geo_method)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (guid) DO UPDATE SET
			type=EXCLUDED.type, sentiment=EXCLUDED.sentiment, language=EXCLUDED.language,
			priority=EXCLUDED.priority, summary=EXCLUDED.summary, source=EXCLUDED.source,
			nearest_office=EXCLUDED.nearest_office, geo_lat=EXCLUDED.geo_lat,
			geo_lon=EXCLUDED.geo_lon, geo_method=EXCLUDED.geo_method`,
		guid, ai.Type, ai.Sentiment, ai.Language, priority, ai.Summary, ai.Source, ai.NearestOffice,
		lat, lon, ai.GeoMethod,
	)
	if err != nil {
		log.Printf("⚠️ DB ai_analysis insert %s: %v", guid[:min(8, len(guid))], err)
	}
}

func saveRoutingToDB(guid string, r RoutingResult) {
	if db == nil {
		return
	}
	_, err := db.Exec(`
		INSERT INTO routing_results (guid, manager_name, manager_role, assigned_office, is_escalated, routing_reason, city_original)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (guid) DO UPDATE SET
			manager_name=EXCLUDED.manager_name, manager_role=EXCLUDED.manager_role,
			assigned_office=EXCLUDED.assigned_office, is_escalated=EXCLUDED.is_escalated,
			routing_reason=EXCLUDED.routing_reason, city_original=EXCLUDED.city_original`,
		guid, r.ManagerName, r.ManagerRole, r.AssignedOffice,
		r.IsEscalated, r.RoutingReason, r.CityOriginal,
	)
	if err != nil {
		log.Printf("⚠️ DB routing_results insert %s: %v", guid[:min(8, len(guid))], err)
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
		knownOffices = append(knownOffices, city)
	}
	fmt.Printf("✅ Офисов загружено: %d → %v\n", len(knownOffices), knownOffices)
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
//  ГЕОКОДИРОВАНИЕ — Nominatim (OpenStreetMap) + Haversine
// ═══════════════════════════════════════════════════════════

// haversine — расстояние между двумя точками в километрах
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// findNearestOfficeByCoords — ближайший офис по координатам (Haversine)
func findNearestOfficeByCoords(lat, lon float64) string {
	bestOffice := ""
	bestDist := 1e18
	for _, office := range knownOffices {
		coords, ok := OfficeCoords[office]
		if !ok {
			continue
		}
		d := haversine(lat, lon, coords.Lat, coords.Lon)
		if d < bestDist {
			bestDist = d
			bestOffice = office
		}
	}
	fmt.Printf("   📐 Haversine: ближайший офис '%s' (%.0f км)\n", bestOffice, bestDist)
	return bestOffice
}

// geocodeAddress — геокодирование через Nominatim OpenStreetMap
// Возвращает (lat, lon, ok). При ошибке ok=false.
func geocodeAddress(country, oblast, city, street, house string) (float64, float64, bool) {
	// Составляем строку запроса из доступных полей
	parts := []string{}
	if house != "" && street != "" {
		parts = append(parts, house+" "+street)
	} else if street != "" {
		parts = append(parts, street)
	}
	if city != "" {
		parts = append(parts, city)
	} else if oblast != "" {
		parts = append(parts, oblast)
	}
	if country != "" {
		parts = append(parts, country)
	}

	if len(parts) == 0 {
		return 0, 0, false
	}

	query := strings.Join(parts, ", ")
	encoded := strings.ReplaceAll(query, " ", "+")
	url := "https://nominatim.openstreetmap.org/search?q=" + encoded + "&format=json&limit=1&countrycodes=kz"

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, 0, false
	}
	// Nominatim требует User-Agent
	req.Header.Set("User-Agent", "FIRE-RoutingEngine/6.0 (freedom.broker)")

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()

	var results []struct {
		Lat string `json:"lat"`
		Lon string `json:"lon"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil || len(results) == 0 {
		return 0, 0, false
	}

	lat, err1 := strconv.ParseFloat(results[0].Lat, 64)
	lon, err2 := strconv.ParseFloat(results[0].Lon, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return lat, lon, true
}

// resolveOfficeForTicket — определяет офис через:
//  1. Nominatim геокодирование + Haversine (приоритет)
//  2. Fallback: LLM-определение (nearest_office из промпта)
func resolveOfficeForTicket(t TicketInput, llmOffice string) (office string, lat, lon float64, method string) {
	isKZ := t.Country == "" ||
		strings.Contains(strings.ToLower(t.Country), "казахстан") ||
		strings.EqualFold(t.Country, "kz") ||
		strings.EqualFold(t.Country, "kazakhstan")

	if !isKZ {
		return "", 0, 0, "foreign"
	}

	// Пробуем Nominatim
	lat, lon, ok := geocodeAddress(t.Country, t.Oblast, t.RawCity, t.Street, t.House)
	if ok {
		fmt.Printf("   🌐 Nominatim: %.4f, %.4f\n", lat, lon)
		nearestOffice := findNearestOfficeByCoords(lat, lon)
		if nearestOffice != "" {
			return nearestOffice, lat, lon, "nominatim"
		}
	}

	// Fallback: LLM-результат
	if llmOffice != "" {
		fmt.Printf("   🤖 LLM-геолокация: офис '%s'\n", llmOffice)
		return llmOffice, 0, 0, "llm"
	}

	return "", 0, 0, "unknown"
}

func fallbackAnalyze(t TicketInput) AIResult {
	text := t.Text + " " + t.Attachment
	lower := strings.ToLower(text)

	r := AIResult{
		Type:          "Консультация",
		Sentiment:     "Нейтральный",
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
		r.Sentiment = "Негативный"
		r.Priority = "10"
		r.Summary = "Клиент угрожает обращением в правоохранительные органы или суд. Немедленная эскалация Главному специалисту."

	case containsAny(text, "мошенник", "украли", "взлом", "несанкционированн", "fraud",
		"scam", "мошеннические", "финансовые махинации"):
		r.Type = "Мошеннические действия"
		r.Sentiment = "Негативный"
		r.Priority = "9"
		r.Summary = "Подозрение на мошенничество или несанкционированные действия. Срочно в отдел безопасности."

	case containsAny(text, "верните", "возврат", "компенсация", "возместите", "refund",
		"не пришло", "не на моем счету", "списали"):
		r.Type = "Претензия"
		r.Sentiment = "Негативный"
		r.Priority = "8"
		r.Summary = "Требование возврата средств. Запросить детали транзакции и подтверждающие документы."

	case containsAny(text, "смена номера", "изменить данные", "паспорт", "реквизиты",
		"смена данных", "изменить номер", "персональные данные", "удалить мои данные"):
		r.Type = "Смена данных"
		r.Sentiment = "Нейтральный"
		r.Priority = "6"
		r.Summary = "Запрос на изменение персональных данных. Запросить документы для верификации."

	case containsAny(text, "не могу войти", "не работает", "вылетает", "зависает",
		"ошибка", "crash", "error", "blocked", "заблокирован", "блокирован",
		"пароль не принимает", "смс не приходит", "код не приходит"):
		r.Type = "Неработоспособность приложения"
		r.Sentiment = "Негативный"
		r.Priority = "6"
		r.Summary = "Технический сбой при входе или работе с приложением. Запросить ОС, версию приложения и скриншоты."

	case containsAny(text, "недоволен", "ужасно", "безобразие", "отвратительно", "terrible",
		"мошеннич", "ведете себя как"):
		r.Type = "Жалоба"
		r.Sentiment = "Негативный"
		r.Priority = "7"
		r.Summary = "Негативная оценка сервиса. Выслушать, принести извинения, предложить решение."

	case containsAny(text, "акция!", "выиграли", "поздравляем вы", "бесплатно!",
		"специальные цены", "питомник", "тюльпаны", "сварочные", "оборудование",
		"ПЕРВОУРАЛЬСКБАНК", "московская биржа", "safelinks", "enkod.ru"):
		r.Type = "Спам"
		r.Priority = "1"
		r.Sentiment = "Нейтральный"
		r.Summary = "Входящее сообщение классифицировано как рекламная рассылка."
	}

	return r
}

// ═══════════════════════════════════════════════════════════
//  БАТЧ AI АНАЛИЗ — один запрос на все тикеты
// ═══════════════════════════════════════════════════════════

// getString — безопасно извлекает строку из map[string]any.
// При null или отсутствии поля возвращает пустую строку вместо "<nil>".
func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

type ticketForPrompt struct {
	Index   int    `json:"i"`
	Text    string `json:"text"`
	Segment string `json:"segment,omitempty"`
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
			Segment: t.Segment,
			Country: t.Country,
			Oblast:  t.Oblast,
			City:    t.RawCity,
		})
	}

	ticketsJSON, _ := json.Marshal(promptTickets)

	prompt := fmt.Sprintf(`Ты — аналитик клиентских обращений Freedom Broker (Казахстан).
Обработай массив тикетов и верни ТОЛЬКО JSON-массив без маркдауна, пояснений и текста вне массива.

ДОСТУПНЫЕ ОФИСЫ (nearest_office СТРОГО только из этого списка, любое другое значение — ОШИБКА):
%s

═══════════════════════════════════════════════════════
ПРАВИЛА КЛАССИФИКАЦИИ ТИПА ОБРАЩЕНИЯ:
═══════════════════════════════════════════════════════
"Жалоба"
  — клиент недоволен качеством сервиса, обслуживанием, сроками, но НЕ требует денег и НЕ угрожает судом
  — примеры: "недоволен работой", "ужасный сервис", "уже неделю не решают", "заблокировали без причины"
  — ОТЛИЧИЕ от Претензии: нет требования денег, нет угрозы судом/прокуратурой

"Смена данных"
  — клиент хочет изменить персональные данные: номер телефона, email, паспорт, адрес, ИИН
  — примеры: "хочу изменить номер", "смена телефона", "обновить паспортные данные", "удалить мои данные"

"Консультация"
  — клиент задаёт вопрос, хочет получить информацию, уточнить условия — НЕТ технической проблемы
  — примеры: "как купить акции", "какие комиссии", "можно ли дробно", "что такое ETF", "ИИН компании"
  — ОТЛИЧИЕ от Неработоспособности: клиент может пользоваться системой, просто задаёт вопрос

"Претензия"
  — клиент требует возврата денег/компенсации ИЛИ угрожает судом/прокуратурой/журналистами
  — примеры: "верните деньги", "подам в суд", "обращусь в прокуратуру", "125$ не пришло верните"
  — ОТЛИЧИЕ от Жалобы: есть конкретное денежное требование ИЛИ угроза правовыми действиями

"Неработоспособность приложения"
  — технические проблемы мешают клиенту ИСПОЛЬЗОВАТЬ сервис: не входит, не приходит SMS, ошибка
  — примеры: "не могу войти", "пароль не принимает", "смс не приходит", "ошибка при входе", "не могу зарегистрироваться"
  — ОТЛИЧИЕ от Консультации: клиент ПЫТАЕТСЯ что-то сделать, но система не даёт

"Мошеннические действия"
  — клиент подозревает мошенничество, несанкционированный доступ, просит проверить легитимность
  — примеры: "не мошенники ли вы", "взломали аккаунт", "Money Advisor мошенники?", "проверьте сертификат"

"Спам"
  — рекламные рассылки, предложения от третьих компаний, не связанные с Freedom Broker
  — примеры: тюльпаны, сварочные агрегаты, ПЕРВОУРАЛЬСКБАНК, ссылки safelinks.protection.outlook.com

═══════════════════════════════════════════════════════
ПРАВИЛА ТОНАЛЬНОСТИ:
═══════════════════════════════════════════════════════
"Негативный" — явное недовольство, угрозы, требования, обвинения, срочность с давлением
"Позитивный" — благодарность, похвала, удовлетворённость
"Нейтральный" — нейтральный вопрос, запрос информации, техническая проблема без эмоций, спам

═══════════════════════════════════════════════════════
ПРАВИЛА ПРИОРИТЕТА (1–10):
═══════════════════════════════════════════════════════
10 — Претензия с угрозой суда/прокуратуры/правоохранителей | ЛЮБОЕ обращение VIP или Priority сегмента
 9 — Мошеннические действия (подозрение на взлом, фрод)
 8 — Претензия (требование возврата денег без угрозы суда)
 7 — Жалоба с явным давлением ("срочно!", "требую", "в течение часа")
 6 — Жалоба, Неработоспособность приложения, Смена данных
 5 — Консультация стандартная
 3 — Консультация общая, низкий приоритет
 1 — Спам

ВАЖНО: Если поле segment тикета = "VIP" или "Priority" — приоритет ВСЕГДА 10, независимо от содержания.

═══════════════════════════════════════════════════════
ЯЗЫК:
═══════════════════════════════════════════════════════
"KZ" — казахский (саламатсыздарма, қандай, алуға, бұйрық, неге, рахмет, сіз, өтінемін)
"ENG" — английский (hello, please, help, I am, my account, unable, verification)
"RU" — русский или если язык не определён

═══════════════════════════════════════════════════════
SUMMARY (поле "summary"):
═══════════════════════════════════════════════════════
1–2 предложения: суть обращения + рекомендация менеджеру.
ОБЯЗАТЕЛЬНОЕ ПРАВИЛО ЯЗЫКА SUMMARY — нарушение = ошибка:
  — если language="KZ" → summary пиши ТОЛЬКО на казахском языке (қазақ тілінде)
  — если language="ENG" → summary write ONLY in English
  — если language="RU"  → summary пиши ТОЛЬКО на русском

ПРИМЕРЫ ПРАВИЛЬНОГО SUMMARY:
  language="KZ": "Клиент қосымшаға кіре алмайды, SMS коды келмейді. Техникалық мәселені тексеріп, кодты қайта жіберіп, клиентке хабарласыңыз."
  language="ENG": "Client is unable to access the application due to a technical error. Verify account status and resend the verification SMS."
  language="RU": "Клиент не может войти в приложение — SMS-код не приходит. Проверить статус аккаунта и отправить код повторно."

═══════════════════════════════════════════════════════
ГЕОЛОКАЦИЯ (nearest_office):
═══════════════════════════════════════════════════════
Определи ближайший офис из СПИСКА ВЫШЕ по полям country/oblast/city.
СТРОГОЕ ПРАВИЛО: nearest_office ОБЯЗАН быть одним из значений списка ДОСТУПНЫЕ ОФИСЫ выше.
Если значение не из списка — верни пустую строку "".
Учитывай: опечатки, транслитерацию, исторические названия, пригороды.
Если клиент из-за рубежа (Украина, Россия, Азербайджан и т.д.) → nearest_office: ""
Если адрес в Казахстане, но офис не ясен → nearest_office: ""

Примеры:
  Алматинская обл, Тургень → "Алматы"
  г.Алматы / Алматы / Алматинская → "Алматы"
  Акмолинская, Косшы → "Астана"
  Акмолинская, Кокшетау → "Кокшетау"
  Акмолинская, Красный Яр → "Астана"
  Семипалатинская / ВКО, Усть-Каменогорск → "Усть-Каменогорск"
  Восточно-Казахстанская, Кокпекты → "Усть-Каменогорск"
  г.Шымкент / ЮКО / Шымкент обл / Туркестанская → "Шымкент"
  Павлодарская, Аксу → "Павлодар"
  Северо-Казахстанская → "Петропавловск"
  Атырауская, Индербор → "Атырау"
  Mangystau obl., Aktau → "Актау"
  Абайская, Бескарагай → "Усть-Каменогорск"
  Алматинская обл., Конаев (Капчагай) → "Алматы"

═══════════════════════════════════════════════════════
ВЕРНИ ТОЛЬКО JSON-МАССИВ (без markdown и пояснений):
[{"i":<число>,"type":"...","sentiment":"...","language":"...","priority":<1-10>,"summary":"...","nearest_office":"..."}]

ТИКЕТЫ (поле segment передаётся для учёта при расчёте приоритета):
%s`, officesList, string(ticketsJSON))

	body, _ := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]any{{"text": prompt}}},
		},
		"generationConfig": map[string]any{
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

	// Парсинг через any — устойчиво к типу priority (число или строка)
	var rawResults []map[string]any
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
			Type:          getString(item, "type"),
			Sentiment:     getString(item, "sentiment"),
			Language:      getString(item, "language"),
			Priority:      priority,
			Summary:       getString(item, "summary"),
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
		// ── Фильтр 1: VIP/Priority сегмент ИЛИ высокий приоритет → нужен навык VIP
		if needsVIP(segment) || isHighPriority(ai.Priority) {
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
// Геокодирование уже выполнено: ai.NearestOffice содержит финальный офис, ai.GeoMethod — метод.
// Возвращает: менеджер, назначенный офис, флаг эскалации в ГО
func routeTicket(t TicketInput, ai AIResult) (*Manager, string, bool) {
	isKazakhstan := t.Country == "" ||
		strings.Contains(strings.ToLower(t.Country), "казахстан") ||
		strings.EqualFold(t.Country, "kz") ||
		strings.EqualFold(t.Country, "kazakhstan")

	// ── Шаг 1: Определение целевого офиса ────────────────────
	targetOffice := ai.NearestOffice

	if targetOffice == "" || !isKazakhstan || ai.GeoMethod == "foreign" {
		// Клиент из-за рубежа или адрес не определён → 50/50 Астана/Алматы
		if foreignSplitCtr%2 == 0 {
			targetOffice = "Астана"
		} else {
			targetOffice = "Алматы"
		}
		foreignSplitCtr++

		if !isKazakhstan || ai.GeoMethod == "foreign" {
			fmt.Printf("   🌍 Иностранный клиент '%s' → %s (50/50)\n", t.Country, targetOffice)
		} else {
			fmt.Printf("   🌍 Адрес не определён '%s' → %s (50/50)\n", t.RawCity, targetOffice)
		}
	} else {
		switch ai.GeoMethod {
		case "nominatim":
			fmt.Printf("   📍 Nominatim+Haversine: '%s' → офис '%s' (%.4f, %.4f)\n",
				t.RawCity, targetOffice, ai.GeoLat, ai.GeoLon)
		case "llm":
			fmt.Printf("   🤖 LLM-геолокация: '%s' → офис '%s'\n", t.RawCity, targetOffice)
		}
	}

	// ── Шаг 2: Поиск менеджера в целевом офисе ───────────────
	if pool, ok := ManagersMap[targetOffice]; ok {
		if winner := findBestManager(pool, t.Segment, ai, targetOffice); winner != nil {
			return winner, targetOffice, false
		}
		noMatchReason := buildNoMatchReason(t.Segment, ai)
		fmt.Printf("   🔼 В '%s' нет подходящего менеджера (%s) → эскалация в ГО\n", targetOffice, noMatchReason)
	} else {
		fmt.Printf("   🔼 Офис '%s' не найден → эскалация в ГО\n", targetOffice)
	}

	// ── Шаг 3: Эскалация в ГО (Астана или Алматы) ────────────
	for _, hq := range HQ_CITIES {
		if hq == targetOffice {
			continue
		}
		if pool, ok := ManagersMap[hq]; ok {
			if winner := findBestManager(pool, t.Segment, ai, hq); winner != nil {
				fmt.Printf("   🔼 Эскалировано в ГО → %s (%s)\n", hq, winner.Name)
				return winner, hq, true
			}
		}
	}

	// ── Шаг 4: Менеджер не найден ────────────────────────────
	fmt.Printf("   ❌ Менеджер не найден ни в одном офисе\n")
	return nil, "—", false
}

// buildNoMatchReason — формирует читаемую причину отсутствия подходящего менеджера
func buildNoMatchReason(segment string, ai AIResult) string {
	var reasons []string
	if needsVIP(segment) || isHighPriority(ai.Priority) {
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

// buildRoutingReason — формирует читаемую причину успешного роутинга
func buildRoutingReason(segment string, ai AIResult, geoMethod string) string {
	var parts []string
	switch geoMethod {
	case "nominatim":
		parts = append(parts, "Geo:Nominatim+Haversine")
	case "llm":
		parts = append(parts, "Geo:LLM")
	case "50/50", "foreign", "unknown":
		parts = append(parts, "Geo:50/50")
	}
	if needsVIP(segment) {
		parts = append(parts, "VIP-сегмент")
	}
	if isHighPriority(ai.Priority) {
		parts = append(parts, "Высокий приоритет")
	}
	if ai.Type == "Смена данных" {
		parts = append(parts, "Главный специалист")
	}
	if ai.Language == "KZ" || ai.Language == "ENG" {
		parts = append(parts, "Язык:"+ai.Language)
	}
	parts = append(parts, "Round Robin")
	return strings.Join(parts, " → ")
}

// ═══════════════════════════════════════════════════════════
//  ОСНОВНАЯ ОБРАБОТКА ТИКЕТОВ
// ═══════════════════════════════════════════════════════════

// ═══════════════════════════════════════════════════════════
//  ПАРАЛЛЕЛЬНОЕ ГЕОКОДИРОВАНИЕ — кэш + rate limiter
// ═══════════════════════════════════════════════════════════

// geocodeAllParallel геокодирует все тикеты параллельно.
// Соблюдает ограничение Nominatim (1 req/sec) через тикер.
// Одинаковые адреса обслуживаются из кэша без повторных запросов.
func geocodeAllParallel(tickets []TicketInput, aiResults map[int]AIResult) {
	cache := make(map[string]struct {
		office, method string
		lat, lon       float64
	})
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Nominatim: не более 1 запроса в секунду
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	fmt.Printf("🌐 Геокодирование %d тикетов (rate limit 1 req/sec, с кэшем)...\n", len(tickets))

	for i := range tickets {
		t := tickets[i]
		ai := aiResults[t.Index]
		cacheKey := t.Country + "|" + t.Oblast + "|" + t.RawCity + "|" + t.Street + "|" + t.House

		mu.Lock()
		if hit, ok := cache[cacheKey]; ok {
			// Адрес уже геокодирован — берём из кэша
			ai.GeoLat, ai.GeoLon, ai.GeoMethod = hit.lat, hit.lon, hit.method
			if hit.office != "" {
				ai.NearestOffice = hit.office
			}
			aiResults[t.Index] = ai
			mu.Unlock()
			fmt.Printf("   💾 Кэш: '%s' → '%s'\n", t.RawCity, hit.office)
			continue
		}
		mu.Unlock()

		wg.Add(1)
		go func(ticket TicketInput, llmOffice, key string, idx int) {
			defer wg.Done()
			<-ticker.C // ждём свой слот (1 req/sec)
			office, lat, lon, method := resolveOfficeForTicket(ticket, llmOffice)

			mu.Lock()
			cache[key] = struct {
				office, method string
				lat, lon       float64
			}{office, method, lat, lon}
			a := aiResults[idx]
			a.GeoLat, a.GeoLon, a.GeoMethod = lat, lon, method
			if office != "" {
				a.NearestOffice = office
			}
			aiResults[idx] = a
			mu.Unlock()
		}(t, ai.NearestOffice, cacheKey, t.Index)
	}
	wg.Wait()
	fmt.Println("✅ Геокодирование завершено")
}

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

	// ── Заголовок CSV ────────────────────────────────────────────
	if needHeader {
		writer.Write([]string{
			"GUID",
			"Сегмент",
			"Тип",
			"Тональность",
			"Язык",
			"Приоритет",
			"Рекомендации менеджеру",
			"Назначенный Менеджер",
			"Должность",
			"Офис Назначения",
			"Эскалирован",
			"Город_оригинал",
			"Причина_роутинга",
			"AI_Источник",
			"Метод_гео",
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

	// ── ФАЗА 1: Параллельное геокодирование (кэш + 1 req/sec) ───────
	geocodeAllParallel(tickets, aiResults)

	// ── ФАЗА 2: Роутинг + запись ─────────────────────────────────────
	fmt.Println("\n📋 Роутинг тикетов...")
	fmt.Println(strings.Repeat("─", 70))

	var allResults []RoutingResult
	var dbWg sync.WaitGroup // DB saves идут асинхронно, не блокируя CSV

	for _, t := range tickets {
		ai := aiResults[t.Index]
		shortGUID := t.GUID
		if len(t.GUID) > 8 {
			shortGUID = t.GUID[:8]
		}

		fmt.Printf("\n[%d/%d] %s | %s | %s | приор.%s | офис:'%s' [%s]\n",
			t.Index+1, len(tickets), shortGUID, t.RawCity, ai.Type, ai.Priority,
			ai.NearestOffice, ai.GeoMethod)

		var routingResult RoutingResult

		// ── СПАМ: сохраняем для аналитики, менеджер не назначается ──
		if ai.Type == "Спам" {
			fmt.Printf("   🚫 Спам — менеджер не назначается\n")
			routingResult = RoutingResult{
				GUID:           t.GUID,
				CityOriginal:   t.RawCity,
				Segment:        t.Segment,
				Type:           ai.Type,
				Sentiment:      ai.Sentiment,
				Language:       ai.Language,
				Priority:       ai.Priority,
				Summary:        ai.Summary,
				ManagerName:    "—",
				ManagerRole:    "—",
				AssignedOffice: "—",
				RoutingReason:  "Спам — менеджер не назначается",
				GeoMethod:      ai.GeoMethod,
				Source:         ai.Source,
				IsEscalated:    false,
			}
		} else {
			winner, assignedOffice, isEscalated := routeTicket(t, ai)
			managerName, managerRole := "Не найден", "—"
			routingReason := buildNoMatchReason(t.Segment, ai)
			if winner != nil {
				managerName = winner.Name
				managerRole = winner.Role
				routingReason = buildRoutingReason(t.Segment, ai, ai.GeoMethod)
				fmt.Printf("   🎯 %s (%s) → офис %s\n", managerName, managerRole, assignedOffice)
			} else {
				fmt.Printf("   ❌ Менеджер не найден\n")
			}

			routingResult = RoutingResult{
				GUID:           t.GUID,
				CityOriginal:   t.RawCity,
				Segment:        t.Segment,
				Type:           ai.Type,
				Sentiment:      ai.Sentiment,
				Language:       ai.Language,
				Priority:       ai.Priority,
				Summary:        ai.Summary,
				ManagerName:    managerName,
				ManagerRole:    managerRole,
				AssignedOffice: assignedOffice,
				RoutingReason:  routingReason,
				GeoMethod:      ai.GeoMethod,
				Source:         ai.Source,
				IsEscalated:    isEscalated,
			}
		}

		allResults = append(allResults, routingResult)

		// ── Async DB save: не блокируем роутинг следующего тикета ────
		dbWg.Add(1)
		go func(ticket TicketInput, aiSnap AIResult, rr RoutingResult) {
			defer dbWg.Done()
			saveTicketToDB(ticket)
			saveAIResultToDB(ticket.GUID, aiSnap)
			saveRoutingToDB(ticket.GUID, rr)
		}(t, ai, routingResult)

		// ── CSV write (последовательно — порядок важен) ───────────────
		escalatedStr := "Нет"
		if routingResult.IsEscalated {
			escalatedStr = "Да"
		}
		writer.Write([]string{
			routingResult.GUID,
			routingResult.Segment,
			routingResult.Type,
			routingResult.Sentiment,
			routingResult.Language,
			routingResult.Priority,
			routingResult.Summary,
			routingResult.ManagerName,
			routingResult.ManagerRole,
			routingResult.AssignedOffice,
			escalatedStr,
			routingResult.CityOriginal,
			routingResult.RoutingReason,
			routingResult.Source,
			routingResult.GeoMethod,
		})
		writer.Flush()
	}

	dbWg.Wait() // Ждём завершения всех DB-горутин перед выходом

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
	noManager := 0
	spam := 0
	escalated := 0

	for _, r := range results {
		typeCounts[r.Type]++
		sentimentCounts[r.Sentiment]++
		officeCounts[r.AssignedOffice]++
		if r.ManagerName == "Не найден" {
			noManager++
		}
		if r.Type == "Спам" {
			spam++
		}
		if r.IsEscalated {
			escalated++
		}
	}

	fmt.Printf("  Всего обработано: %d\n", len(results))
	fmt.Printf("  Спам:             %d\n", spam)
	fmt.Printf("  Эскалировано в ГО:%d\n", escalated)
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
