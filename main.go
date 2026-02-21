package datasaur

// Основные структуры для In-Memory хранилища
type Manager struct {
	ID       string
	Name     string
	Role     int      // 1: Спец, 2: Ведущий, 3: Главный
	Skills   []string // "VIP", "ENG", "KZ"
	Office   string
	Workload int // Динамический счетчик нагрузки
}

type Ticket struct {
	GUID        string
	Description string
	Attachment  string
	City        string
	Lat, Lon    float64
	// Поля, которые заполнит AI:
	AIType      string
	AISentiment string
	AIPriority  int
}

// Global State
var ManagersMap map[string][]*Manager            // Ключ: Город, Значение: Пул менеджеров
var GeoMap map[string]struct{ Lat, Lon float64 } // Ключ: Город, Значение: Координаты
