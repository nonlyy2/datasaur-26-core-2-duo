import streamlit as st
import pandas as pd
import os
import json
import google.generativeai as genai

st.set_page_config(page_title="FIRE Dashboard", layout="wide", page_icon="🔥")

st.title("🔥 FIRE — Freedom Intelligent Routing Engine")
st.markdown("Система автоматического распределения обращений клиентов | **Freedom Broker**")

# ─── Загрузка данных ───────────────────────────────────────────────────────────
data_path = "data/results.csv"

if not os.path.exists(data_path):
    st.warning("⚠️ Файл results.csv не найден. Запустите Go-движок (`go run main.go`).")
    st.stop()

df = pd.read_csv(data_path)

# Обратная совместимость со старым форматом
if "Офис_назначения" not in df.columns:
    df["Офис_назначения"] = df.get("Город", "—")
if "AI_Источник" not in df.columns:
    df["AI_Источник"] = "Gemini"
if "AI_Summary" not in df.columns:
    df["AI_Summary"] = "—"
if "Причина_роутинга" not in df.columns:
    df["Причина_роутинга"] = "—"

# Конвертируем приоритет в число, если пришёл в числовом виде
def prio_label(val):
    try:
        n = int(float(val))
        if n >= 8:
            return "High"
        elif n >= 5:
            return "Medium"
        else:
            return "Low"
    except:
        return str(val)

df["Приоритет_уровень"] = df["AI_Приоритет"].apply(prio_label)

# ─── МЕТРИКИ ──────────────────────────────────────────────────────────────────
st.subheader("📊 Оперативная сводка")
c1, c2, c3, c4, c5, c6 = st.columns(6)

total = len(df)
vip_count = len(df[df["Сегмент"].isin(["VIP", "Priority"])])
spam_count = len(df[df["AI_Тип"] == "Спам"])
legal_count = len(df[df["AI_Тональность"] == "Legal Risk"])
esc_count = len(df[df["Офис_назначения"].str.contains("ГО", na=False)])
fallback_count = len(df[df["AI_Источник"] == "Fallback"])

c1.metric("Всего тикетов", total)
c2.metric("VIP + Priority", vip_count)
c3.metric("🚨 Спам", spam_count)
c4.metric("⚖️ Legal Risk", legal_count)
c5.metric("🔼 Эскалировано в ГО", esc_count)
c6.metric("🔄 Keyword Fallback", fallback_count)

# ─── ГРАФИКИ ──────────────────────────────────────────────────────────────────
st.markdown("---")
col1, col2, col3 = st.columns(3)

with col1:
    st.subheader("Типы обращений")
    st.bar_chart(df["AI_Тип"].value_counts())

with col2:
    st.subheader("Куда ушли тикеты")
    st.bar_chart(df["Офис_назначения"].value_counts())

with col3:
    st.subheader("Уровни приоритета")
    prio_colors = df["Приоритет_уровень"].value_counts()
    st.bar_chart(prio_colors)

st.markdown("---")
col4, col5 = st.columns(2)

with col4:
    st.subheader("Нагрузка на менеджеров (топ-10)")
    mgr_df = df[df["Назначенный_Менеджер"] != "Не найден"]
    if not mgr_df.empty:
        st.bar_chart(mgr_df["Назначенный_Менеджер"].value_counts().head(10))

with col5:
    st.subheader("Причины роутинга")
    st.bar_chart(df["Причина_роутинга"].value_counts())

# ─── ФИЛЬТРЫ + ТАБЛИЦА ────────────────────────────────────────────────────────
st.markdown("---")
st.subheader("📋 Детализация распределения")

cf1, cf2, cf3, cf4 = st.columns(4)
with cf1:
    f_city = st.multiselect("🏙️ Город", sorted(df["Город_оригинал"].dropna().unique()) if "Город_оригинал" in df.columns else [])
with cf2:
    f_type = st.multiselect("📌 Тип обращения", sorted(df["AI_Тип"].dropna().unique()))
with cf3:
    f_prio = st.multiselect("🔥 Приоритет", ["High", "Medium", "Low"])
with cf4:
    f_seg = st.multiselect("👤 Сегмент", sorted(df["Сегмент"].dropna().unique()))

fdf = df.copy()
city_col = "Город_оригинал" if "Город_оригинал" in df.columns else "Город"
if f_city:
    fdf = fdf[fdf[city_col].isin(f_city)]
if f_type:
    fdf = fdf[fdf["AI_Тип"].isin(f_type)]
if f_prio:
    fdf = fdf[fdf["Приоритет_уровень"].isin(f_prio)]
if f_seg:
    fdf = fdf[fdf["Сегмент"].isin(f_seg)]

def highlight_row(row):
    styles = [""] * len(row)
    idx = row.index.tolist()
    if "Приоритет_уровень" in idx:
        i = idx.index("Приоритет_уровень")
        v = row["Приоритет_уровень"]
        styles[i] = "color: red; font-weight: bold" if v == "High" else "color: orange" if v == "Medium" else "color: green"
    if "AI_Тональность" in idx and row["AI_Тональность"] == "Legal Risk":
        styles[idx.index("AI_Тональность")] = "color: red; font-weight: bold"
    if "Назначенный_Менеджер" in idx and row["Назначенный_Менеджер"] == "Не найден":
        styles[idx.index("Назначенный_Менеджер")] = "background-color: #ffcccc"
    if "Офис_назначения" in idx and "ГО" in str(row["Офис_назначения"]):
        styles[idx.index("Офис_назначения")] = "color: #e67e22; font-weight: bold"
    return styles

show_cols = [c for c in [
    city_col, "Офис_назначения", "Сегмент", "AI_Тип", "AI_Тональность",
    "AI_Язык", "AI_Приоритет", "Приоритет_уровень", "AI_Summary",
    "Назначенный_Менеджер", "Должность", "Причина_роутинга", "AI_Источник"
] if c in fdf.columns]

st.dataframe(fdf[show_cols].style.apply(highlight_row, axis=1), use_container_width=True, height=450)
st.caption(f"Показано {len(fdf)} из {total} тикетов")

# Блок эскалированных тикетов
esc_df = df[df["Офис_назначения"].str.contains("ГО", na=False)]
if not esc_df.empty:
    with st.expander(f"🔼 Эскалированные тикеты ({len(esc_df)}шт) — нажмите для просмотра"):
        esc_cols = [c for c in [city_col, "Сегмент", "AI_Тип", "AI_Приоритет", "Назначенный_Менеджер", "Офис_назначения"] if c in esc_df.columns]
        st.dataframe(esc_df[esc_cols], use_container_width=True)

# ─── STAR TASK: AI АССИСТЕНТ ──────────────────────────────────────────────────
st.markdown("---")
st.subheader("🤖 AI-Ассистент (Star Task)")
st.markdown("Задайте вопрос по данным на естественном языке. Ассистент построит анализ и при необходимости сгенерирует график.")

# Инициализация истории чата
if "chat_history" not in st.session_state:
    st.session_state.chat_history = []

# Показываем историю сообщений
for msg in st.session_state.chat_history:
    with st.chat_message(msg["role"]):
        st.markdown(msg["content"])

# Поле ввода
user_input = st.chat_input("Например: Покажи распределение типов обращений по городам")

if user_input:
    # Добавляем сообщение пользователя
    st.session_state.chat_history.append({"role": "user", "content": user_input})
    with st.chat_message("user"):
        st.markdown(user_input)

    # Контекст для AI: статистика по датасету
    data_context = f"""
Датасет FIRE Dashboard: {total} тикетов.
Столбцы: {', '.join(df.columns.tolist())}
Уникальные типы обращений: {df['AI_Тип'].value_counts().to_dict()}
Тональности: {df['AI_Тональность'].value_counts().to_dict()}
Офисы назначения: {df['Офис_назначения'].value_counts().to_dict()}
Сегменты: {df['Сегмент'].value_counts().to_dict()}
Уровни приоритета: {df['Приоритет_уровень'].value_counts().to_dict()}
Менеджеры (топ-5): {df[df['Назначенный_Менеджер'] != 'Не найден']['Назначенный_Менеджер'].value_counts().head(5).to_dict()}
    """.strip()

    system_prompt = f"""Ты — аналитический AI-ассистент дашборда FIRE (Freedom Intelligent Routing Engine).
Ты помогаешь операторам анализировать данные по тикетам клиентов.
Отвечай кратко и по делу на русском языке.

ДАННЫЕ ПО ДАТАСЕТУ:
{data_context}

Если вопрос про графики/визуализацию — опиши выводы словами (у тебя нет доступа к Matplotlib, но дашборд уже показывает графики выше).
Если вопрос — аналитический — дай конкретный ответ с цифрами из датасета."""

    # Вызов Gemini API
    try:
        gemini_api_key = os.getenv("GEMINI_API_KEY", "")
        if not gemini_api_key:
            answer = "⚠️ GEMINI_API_KEY не найден. Добавьте ключ в файл .env"
        else:
            genai.configure(api_key=gemini_api_key)
            model = genai.GenerativeModel("gemma-3-27b-it")

            # Собираем историю в формат Gemini
            history_for_gemini = []
            for m in st.session_state.chat_history[:-1]:  # Без последнего (это новый вопрос)
                role = "user" if m["role"] == "user" else "model"
                history_for_gemini.append({"role": role, "parts": [m["content"]]})

            chat = model.start_chat(history=history_for_gemini)
            response = chat.send_message(f"{system_prompt}\n\nВопрос: {user_input}")
            answer = response.text
    except Exception as e:
        answer = f"⚠️ Ошибка AI-ассистента: {str(e)}"

    # Показываем ответ
    st.session_state.chat_history.append({"role": "assistant", "content": answer})
    with st.chat_message("assistant"):
        st.markdown(answer)

# Примеры запросов
with st.expander("💡 Примеры вопросов к ассистенту"):
    st.markdown("""
- Сколько VIP-клиентов было эскалировано в главный офис?
- Какой тип обращений встречается чаще всего?
- Покажи статистику по Legal Risk тикетам
- Какой менеджер получил больше всего тикетов?
- Сколько тикетов было обработано через keyword fallback?
- Какой процент тикетов получил приоритет High?
    """)