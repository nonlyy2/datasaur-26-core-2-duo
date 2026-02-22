import streamlit as st
import pandas as pd
import psycopg2
import os
import json
import re
import sys
import subprocess
import time
from google import genai
from dotenv import load_dotenv


load_dotenv()

st.set_page_config(page_title="FIRE Dashboard", layout="wide", page_icon="🔥")

st.title("🔥 FIRE — Freedom Intelligent Routing Engine")
st.markdown("Система автоматического распределения обращений клиентов | **Freedom Broker**")

DB_HOST = os.getenv("DB_HOST", "localhost")
DB_PORT = os.getenv("DB_PORT", "5433")
DB_USER = os.getenv("DB_USER", "postgres")
DB_PASS = os.getenv("DB_PASS", "1234")
DB_NAME = os.getenv("DB_NAME", "fire_db")

# ─── Загрузка данных ───────────────────────────────────────────────────────────

@st.cache_data(ttl=60)
def load_managers_from_db():
    """Читает current_load прямо из таблицы routing_manager."""
    try:
        conn = psycopg2.connect(
            host=DB_HOST, port=DB_PORT,
            user=DB_USER, password=DB_PASS,
            dbname=DB_NAME
        )
        df_m = pd.read_sql(
            "SELECT full_name, current_load FROM routing_manager ORDER BY current_load DESC",
            conn
        )
        conn.close()
        return df_m
    except Exception:
        return pd.DataFrame()

@st.cache_data(ttl=60)  # Кэшируем данные на 60 секунд
def load_data_from_db():
    try:
        conn = psycopg2.connect(
            host=DB_HOST, port=DB_PORT,
            user=DB_USER, password=DB_PASS,
            dbname=DB_NAME
        )
        df = pd.read_sql("SELECT * FROM routing_routingresult", conn)
        conn.close()

        # Переименовываем DB-колонки → русские названия из results.csv
        df = df.rename(columns={
            "ai_segment":             "Сегмент",
            "ai_type":                "Тип",
            "ai_sentiment":           "Тональность",
            "ai_language":            "Язык",
            "ai_priority":            "Приоритет",
            "manager_recommendations":"Рекомендации менеджеру",
            "ai_attachments":         "Вложения",
            "manager_name":           "Назначенный Менеджер",
            "manager_position":       "Должность",
            "ai_assigned_office":     "Офис Назначения",
            "city_original":          "Город_оригинал",
            "routing_reason":         "Причина_роутинга",
            "ai_source":              "AI_Источник",
            "geo_method":             "Метод_гео",
        })

        # is_escalated boolean → читаемая строка
        if "is_escalated" in df.columns:
            df["Эскалирован"] = df["is_escalated"].map({True: "Да", False: "Нет"}).fillna("Нет")
            df.drop(columns=["is_escalated"], inplace=True)

        return df
    except Exception as e:
        st.error(f"❌ Ошибка подключения к базе данных PostgreSQL: {e}")
        return pd.DataFrame()
    
# ─── SIDEBAR: рендерим ДО проверки файла — кнопка видна даже без results.csv ──
with st.sidebar:
    st.subheader("⚙️ Управление")
    st.markdown("Запускает Go-движок: анализ тикетов через Gemini + роутинг.")

    if st.button("▶ Запустить анализ", use_container_width=True, type="primary"):
        project_dir = os.path.dirname(os.path.abspath(__file__))
        timer_placeholder = st.empty()
        log_placeholder   = st.empty()

        try:
            start_time = time.time()
            timeout    = 1550  # 155*10sec
            log_lines  = []
            is_timeout = False

            process = subprocess.Popen(
                ["go", "run", "main.go"],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                encoding="utf-8",
                errors="replace",
                cwd=project_dir,
            )

            # ── Потоковое чтение: каждая строка Go сразу появляется на экране ──
            for raw_line in iter(process.stdout.readline, ""):
                elapsed = int(time.time() - start_time)
                if elapsed > timeout:
                    process.kill()
                    is_timeout = True
                    break
                mins, secs = divmod(elapsed, 60)
                timer_placeholder.info(
                    f"⏳ **Go-движок работает** | ⏱️ **{mins:02d}:{secs:02d}**"
                )
                log_lines.append(raw_line.rstrip())
                log_placeholder.code("\n".join(log_lines[-80:]))  # последние 80 строк

            process.stdout.close()
            process.wait()

            # ── Обработка результата ────────────────────────────────────────────
            if is_timeout:
                timer_placeholder.error("⏰ Тайм-аут 5 мин. Процесс принудительно остановлен.")
                log_placeholder.code("\n".join(log_lines[-80:]))

            elif process.returncode == 0:
                
                timer_placeholder.info("⏳ **Загрузка результатов в БД...**")
                # log_placeholder не трогаем — логи остаются видны

                
                load_proc = subprocess.run(
                    [sys.executable, "load_results.py"], 
                    capture_output=True, text=True, cwd=project_dir
                )
                timer_placeholder.empty()

                if load_proc.returncode == 0:
                    st.success("✅ Go-анализ завершён и данные загружены в БД!")
                else:
                    st.warning("⚠️ Go завершил работу, но load_results.py вернул ошибку:")
                    combined = (load_proc.stdout or "") + (load_proc.stderr or "")
                    st.code(combined[-2000:] if combined.strip() else "(нет вывода)")

                st.cache_data.clear()
                if st.button("🔄 Обновить дашборд", type="primary", use_container_width=True):
                    st.rerun()

            else:
                timer_placeholder.error(
                    f"❌ Go завершился с ошибкой (код {process.returncode})"
                )
                log_placeholder.code("\n".join(log_lines[-80:]))

        except FileNotFoundError:
            st.error("❌ `go` не найден. Убедитесь, что Go установлен и добавлен в PATH.")
        except Exception as e:
            st.error(f"❌ Произошла ошибка: {e}")

df = load_data_from_db()
if df.empty:
    st.info("👈 База данных пуста или недоступна. Нажмите **▶ Запустить анализ** в боковой панели.")
    st.stop()


COL_SEG     = "Сегмент"
COL_TYPE    = "Тип"
COL_SENT    = "Тональность"
COL_LANG    = "Язык"
COL_PRIO    = "Приоритет"
COL_SUMMARY = "Рекомендации менеджеру"
COL_MANAGER = "Назначенный Менеджер"
COL_ROLE    = "Должность"
COL_OFFICE  = "Офис Назначения"
COL_ESC     = "Эскалирован"

# Добавляем Язык если отсутствует (старые results.csv)
if COL_LANG not in df.columns:
    df[COL_LANG] = "RU"

# Уровень приоритета
def prio_label(val):
    try:
        n = int(float(val))
        if n >= 8:   return "High"
        elif n >= 5: return "Medium"
        else:        return "Low"
    except:
        return str(val)

df["Приоритет_уровень"] = df[COL_PRIO].apply(prio_label)

with st.sidebar:
    st.markdown("---")
    if st.button("🔄 Обновить данные", use_container_width=True):
        st.cache_data.clear()
        st.rerun()
    st.caption(f"🗄️ БД: `{DB_NAME}` на `{DB_HOST}:{DB_PORT}`")
    st.caption(f"📊 Тикетов в БД: {len(df)}")
    expected = {COL_MANAGER, COL_ROLE, COL_ESC, "Город_оригинал", "Причина_роутинга"}
    missing = expected - set(df.columns)
    if missing:
        st.warning(f"⚠️ Отсутствуют колонки: {', '.join(sorted(missing))}\n\nЗапустите:\n```\npython manage.py migrate\npython load_results.py\n```")

# ─── МЕТРИКИ ──────────────────────────────────────────────────────────────────
st.subheader("📊 Оперативная сводка")
c1, c2, c3, c4, c5 = st.columns(5)

total          = len(df)
vip_count      = len(df[df[COL_SEG].isin(["VIP", "Priority"])])
spam_count     = len(df[df[COL_TYPE] == "Спам"])
highrisk_count = len(df[df[COL_TYPE].isin(["Претензия", "Мошеннические действия"])])
esc_count      = len(df[df[COL_ESC] == "Да"]) if COL_ESC in df.columns else 0

c1.metric("Всего тикетов",        total)
c2.metric("VIP + Priority",       vip_count)
c3.metric("🚨 Спам",              spam_count)
c4.metric("⚖️ Претензии / Фрод", highrisk_count)
c5.metric("🔼 Эскалировано в ГО", esc_count)

# ─── ГРАФИКИ ──────────────────────────────────────────────────────────────────
@st.cache_data(ttl=60)
def load_manager_loads():
    try:
        conn = psycopg2.connect(host=DB_HOST, port=DB_PORT, user=DB_USER, password=DB_PASS, dbname=DB_NAME)
        df_mgr = pd.read_sql("SELECT full_name, current_load FROM routing_manager ORDER BY current_load DESC LIMIT 10", conn)
        conn.close()
        return df_mgr
    except Exception:
        return pd.DataFrame(columns=["full_name", "current_load"])

st.markdown("---")
col1, col2, col3 = st.columns(3)

with col1:
    st.subheader("Типы обращений")
    st.bar_chart(df[COL_TYPE].value_counts())

with col2:
    st.subheader("Куда ушли тикеты")
    st.bar_chart(df[COL_OFFICE].value_counts())

with col3:
    st.subheader("Уровни приоритета")
    st.bar_chart(df["Приоритет_уровень"].value_counts())

st.markdown("---")
col4, col5 = st.columns(2)

with col4:
    st.subheader("Нагрузка на менеджеров (топ-10)")
    df_mgr_load = load_managers_from_db()
    if not df_mgr_load.empty and "current_load" in df_mgr_load.columns:
        top10 = df_mgr_load[df_mgr_load["current_load"] > 0].head(10).set_index("full_name")
        if not top10.empty:
            st.bar_chart(top10["current_load"])
        else:
            st.info("Нагрузка на менеджеров равна нулю.")
    else:
        st.info("Нет данных о нагрузке менеджеров.")

with col5:
    st.subheader("Тональность обращений")
    st.bar_chart(df[COL_SENT].value_counts())

# ─── ФИЛЬТРЫ + ТАБЛИЦА ────────────────────────────────────────────────────────
st.markdown("---")
st.subheader("📋 Детализация распределения")

cf1, cf2, cf3, cf4 = st.columns(4)
with cf1:
    f_type = st.multiselect("📌 Тип обращения", sorted(df[COL_TYPE].dropna().unique()))
with cf2:
    f_prio = st.multiselect("🔥 Приоритет",     ["High", "Medium", "Low"])
with cf3:
    f_seg  = st.multiselect("👤 Сегмент",        sorted(df[COL_SEG].dropna().unique()))
with cf4:
    f_off  = st.multiselect("🏢 Офис",           sorted(df[COL_OFFICE].dropna().unique()))

fdf = df.copy()
if f_type: fdf = fdf[fdf[COL_TYPE].isin(f_type)]
if f_prio: fdf = fdf[fdf["Приоритет_уровень"].isin(f_prio)]
if f_seg:  fdf = fdf[fdf[COL_SEG].isin(f_seg)]
if f_off:  fdf = fdf[fdf[COL_OFFICE].isin(f_off)]

def highlight_row(row):
    styles = [""] * len(row)
    idx = row.index.tolist()
    if "Приоритет_уровень" in idx:
        i = idx.index("Приоритет_уровень")
        v = row["Приоритет_уровень"]
        styles[i] = ("color: red; font-weight: bold" if v == "High"
                     else "color: orange" if v == "Medium"
                     else "color: green")
    if COL_SENT in idx and row[COL_SENT] == "Legal Risk":
        styles[idx.index(COL_SENT)] = "color: red; font-weight: bold"
    if COL_MANAGER in idx and row[COL_MANAGER] == "Не найден":
        styles[idx.index(COL_MANAGER)] = "background-color: #ffcccc"
    if COL_ESC in idx and row.get(COL_ESC) == "Да":
        styles[idx.index(COL_ESC)] = "color: #e67e22; font-weight: bold"
    return styles

show_cols = [c for c in [
    COL_SEG, COL_TYPE, COL_SENT, COL_LANG,
    COL_PRIO, "Приоритет_уровень", COL_SUMMARY,
    COL_MANAGER, COL_ROLE, COL_OFFICE, COL_ESC
] if c in fdf.columns]

st.dataframe(
    fdf[show_cols].style.apply(highlight_row, axis=1),
    use_container_width=True,
    height=450
)
st.caption(f"Показано {len(fdf)} из {total} тикетов")

# Блок эскалированных тикетов
if COL_ESC in df.columns:
    esc_df = df[df[COL_ESC] == "Да"]
else:
    esc_df = df[df[COL_OFFICE].str.contains("ГО", na=False)]
if not esc_df.empty:
    with st.expander(f"🔼 Эскалированные тикеты ({len(esc_df)} шт) — нажмите для просмотра"):
        esc_cols = [c for c in [COL_SEG, COL_TYPE, COL_PRIO, COL_MANAGER, COL_OFFICE, COL_ESC]
                    if c in esc_df.columns]
        st.dataframe(esc_df[esc_cols], use_container_width=True)

# ─── ПРОСМОТР ВЛОЖЕНИЙ ────────────────────────────────────────────────────────
if "Вложения" in df.columns:
    attach_df = df[df["Вложения"].notna() & (df["Вложения"] != "")]
    if not attach_df.empty:
        st.markdown("---")
        st.subheader("🖼️ Вложения к тикетам")

        ticket_ids = attach_df["ticket_id"].astype(str).tolist() if "ticket_id" in attach_df.columns else attach_df.index.astype(str).tolist()
        labels     = attach_df["Вложения"].tolist()
        options    = [f"{tid} — {lbl}" for tid, lbl in zip(ticket_ids, labels)]

        selected = st.selectbox("Выберите тикет с вложением:", options)
        if selected:
            sel_idx  = options.index(selected)
            att_path = attach_df.iloc[sel_idx]["Вложения"]

            project_dir = os.path.dirname(os.path.abspath(__file__))

            # Пробуем определить: URL или локальный путь
            if att_path.startswith("http://") or att_path.startswith("https://"):
                try:
                    st.image(att_path, use_container_width=True)
                except Exception as e:
                    st.error(f"Не удалось загрузить изображение по URL: {e}")
            else:
                # Ищем файл: сначала как есть, потом в папках data/ и attachments/
                candidates = [
                    att_path,
                    os.path.join(project_dir, att_path),
                    os.path.join(project_dir, "data", "attachments", att_path),
                    os.path.join(project_dir, "data", att_path),
                    os.path.join(project_dir, "attachments", att_path),
                ]
                found = next((p for p in candidates if os.path.exists(p)), None)
                if found:
                    ext = os.path.splitext(found)[1].lower()
                    if ext in (".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp"):
                        st.image(found, use_container_width=True)
                    else:
                        st.info(f"📎 Вложение не является изображением: `{att_path}`")
                        with open(found, "rb") as f:
                            st.download_button("⬇️ Скачать файл", f, file_name=os.path.basename(found))
                else:
                    st.warning(f"⚠️ Файл не найден: `{att_path}`")

# ─── STAR TASK: AI АССИСТЕНТ ──────────────────────────────────────────────────
st.markdown("---")
st.subheader("🤖 AI-Ассистент (Star Task)")
st.markdown("Задайте вопрос по данным на естественном языке. Ассистент построит анализ и сгенерирует график.")

# ── Вспомогательные функции ───────────────────────────────────────────────────



def extract_chart_spec(text: str):
    """Извлекает JSON-спецификацию графика из ответа AI, если она есть."""
    match = re.search(r'```json\s*(\{.*?\})\s*```', text, re.DOTALL)
    if not match:
        return None
    try:
        spec = json.loads(match.group(1))
        if spec.get("action") == "chart":
            return spec
    except (json.JSONDecodeError, AttributeError):
        pass
    return None

def strip_json_block(text: str) -> str:
    """Убирает JSON-блок из текста, оставляя только читаемую часть ответа."""
    return re.sub(r'```json\s*\{.*?\}\s*```', '', text, flags=re.DOTALL).strip()

def render_chart_from_spec(spec: dict, source_df: pd.DataFrame):
    """Рендерит Streamlit-график по спецификации от AI."""
    chart_type = spec.get("chart_type", "bar")
    title      = spec.get("title", "График")
    group_by   = spec.get("group_by")
    filter_col = spec.get("filter_col")
    filter_val = spec.get("filter_val")
    top_n      = spec.get("top_n")

    plot_df = source_df.copy()

    # Применяем фильтр, если задан (filter_val может быть строкой или списком)
    if filter_col and filter_val and filter_col in plot_df.columns:
        if isinstance(filter_val, list):
            plot_df = plot_df[plot_df[filter_col].isin(filter_val)]
        else:
            plot_df = plot_df[plot_df[filter_col] == filter_val]

    st.markdown(f"**{title}**")

    if isinstance(group_by, list) and len(group_by) == 2:
        # Кросс-таблица по двум колонкам
        col_a, col_b = group_by
        if col_a in plot_df.columns and col_b in plot_df.columns:
            pivot = pd.crosstab(plot_df[col_a], plot_df[col_b])
            if top_n:
                pivot = pivot.head(top_n)
            st.bar_chart(pivot)
        else:
            st.warning(f"Колонки '{col_a}' или '{col_b}' не найдены в данных.")
    elif isinstance(group_by, str) and group_by in plot_df.columns:
        # Простой подсчёт по одной колонке
        data = plot_df[group_by].value_counts()
        if top_n:
            data = data.head(top_n)
        if chart_type == "line":
            st.line_chart(data)
        else:
            st.bar_chart(data)
    else:
        st.warning(f"Не удалось построить график: колонка '{group_by}' не найдена.")

# ── Состояние чата ────────────────────────────────────────────────────────────

if "chat_history" not in st.session_state:
    st.session_state.chat_history = []  # каждый элемент: {role, content, chart_spec?}

# Воспроизводим историю (текст + графики)
for msg in st.session_state.chat_history:
    with st.chat_message(msg["role"]):
        st.markdown(msg["content"])
        if msg.get("chart_spec"):
            render_chart_from_spec(msg["chart_spec"], df)

# ── Контекст датасета для промпта ─────────────────────────────────────────────

AVAILABLE_COLS = ", ".join([
    COL_SEG, COL_TYPE, COL_SENT, COL_LANG,
    COL_PRIO, "Приоритет_уровень", COL_MANAGER, COL_ROLE, COL_OFFICE, COL_ESC
])

data_context = f"""Датасет FIRE Dashboard: {total} тикетов.
Доступные колонки для group_by: {AVAILABLE_COLS}
Типы обращений: {df[COL_TYPE].value_counts().to_dict()}
Тональности: {df[COL_SENT].value_counts().to_dict()}
Офисы назначения: {df[COL_OFFICE].value_counts().to_dict()}
Сегменты: {df[COL_SEG].value_counts().to_dict()}
Уровни приоритета: {df["Приоритет_уровень"].value_counts().to_dict()}
Менеджеры (топ-5): {df[df[COL_MANAGER] != 'Не найден'][COL_MANAGER].value_counts().head(5).to_dict()}"""

system_prompt = f"""Ты — аналитический AI-ассистент дашборда FIRE (Freedom Intelligent Routing Engine).
Ты помогаешь операторам анализировать данные по тикетам клиентов.
Отвечай кратко и по делу на русском языке.

ДАННЫЕ ПО ДАТАСЕТУ:
{data_context}

ПРАВИЛА ОТВЕТА:
1. Если пользователь просит показать/построить ГРАФИК или ДИАГРАММУ — напиши 1-2 предложения с выводом, а затем добавь JSON-блок в точно таком формате:
```json
{{"action": "chart", "chart_type": "bar", "title": "Название графика", "group_by": "Название_колонки", "filter_col": null, "filter_val": null, "top_n": 10}}
```
Для сравнения двух колонок используй: "group_by": ["КолонкаА", "КолонкаБ"]
Допустимые значения chart_type: "bar", "line"
Используй ТОЛЬКО колонки из списка выше.

2. Если вопрос аналитический — дай конкретный ответ с цифрами. Без JSON-блока."""

# ── Обработка ввода ───────────────────────────────────────────────────────────

user_input = st.chat_input("Например: Покажи распределение типов обращений по офисам")

if user_input:
    st.session_state.chat_history.append({"role": "user", "content": user_input})
    with st.chat_message("user"):
        st.markdown(user_input)

    answer = ""
    chart_spec = None

    try:
        gemini_api_key = os.getenv("GEMINI_API_KEY", "")
        if not gemini_api_key:
            answer = "⚠️ GEMINI_API_KEY не найден. Добавьте ключ в файл .env"
        else:
            client = genai.Client(api_key=gemini_api_key)

            # Передаём только текстовую часть истории в Gemini
            history_for_gemini = []
            for m in st.session_state.chat_history[:-1]:
                role = "user" if m["role"] == "user" else "model"
                history_for_gemini.append({"role": role, "parts": [{"text": m["content"]}]})

            chat = client.chats.create(model="gemini-2.5-flash", history=history_for_gemini)
            response = chat.send_message(f"{system_prompt}\n\nВопрос: {user_input}")
            raw_answer = response.text

            chart_spec = extract_chart_spec(raw_answer)
            answer = strip_json_block(raw_answer) if chart_spec else raw_answer

    except Exception as e:
        answer = f"⚠️ Ошибка AI-ассистента: {str(e)}"

    st.session_state.chat_history.append({
        "role": "assistant",
        "content": answer,
        "chart_spec": chart_spec
    })
    with st.chat_message("assistant"):
        st.markdown(answer)
        if chart_spec:
            render_chart_from_spec(chart_spec, df)

with st.expander("💡 Примеры вопросов к ассистенту"):
    st.markdown("""
**Графики и диаграммы:**
- Покажи распределение типов обращений по офисам
- Построй график нагрузки на менеджеров
- Покажи тональность обращений по сегментам
- Построй диаграмму языков обращений
- Покажи топ-5 самых загруженных офисов

**Аналитика:**
- Сколько VIP-клиентов было эскалировано в главный офис?
- Какой процент тикетов получил приоритет High?
- Какой менеджер ведёт больше всего тикетов?
- Сколько тикетов помечены как спам и в каких офисах они сконцентрированы?
- Есть ли связь между сегментом клиента и тональностью обращения?
- Какие типы обращений чаще всего эскалируются?
- Покажи статистику по эскалированным тикетам: по сегменту и типу
    """)