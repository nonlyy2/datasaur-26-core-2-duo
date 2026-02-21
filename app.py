import streamlit as st
import pandas as pd
import os
from google import genai
from google.genai import types

st.set_page_config(page_title="FIRE Dashboard", layout="wide", page_icon="🔥")

st.title("🔥 FIRE — Freedom Intelligent Routing Engine")
st.markdown("Система автоматического распределения обращений клиентов | **Freedom Broker**")

# ─── Загрузка данных ───────────────────────────────────────────────────────────
data_path = "data/results.csv"

if not os.path.exists(data_path):
    st.warning("⚠️ Файл results.csv не найден. Запустите Go-движок (`go run main.go`).")
    st.stop()

df = pd.read_csv(data_path)

# Колонки из main.go:
# GUID, Сегмент, Тип, Тональность, Язык, Приоритет,
# Рекомендации менеджеру, Назначенный Менеджер, Должность, Офис Назначения

COL_SEG     = "Сегмент"
COL_TYPE    = "Тип"
COL_SENT    = "Тональность"
COL_LANG    = "Язык"
COL_PRIO    = "Приоритет"
COL_SUMMARY = "Рекомендации менеджеру"
COL_MANAGER = "Назначенный Менеджер"
COL_ROLE    = "Должность"
COL_OFFICE  = "Офис Назначения"

if COL_LANG not in df.columns:
    df[COL_LANG] = "RU"

def prio_label(val):
    try:
        n = int(float(val))
        if n >= 8:   return "High"
        elif n >= 5: return "Medium"
        else:        return "Low"
    except:
        return str(val)

df["Приоритет_уровень"] = df[COL_PRIO].apply(prio_label)

# ─── МЕТРИКИ ──────────────────────────────────────────────────────────────────
st.subheader("📊 Оперативная сводка")
c1, c2, c3, c4, c5 = st.columns(5)

total       = len(df)
vip_count   = len(df[df[COL_SEG].isin(["VIP", "Priority"])])
spam_count  = len(df[df[COL_TYPE] == "Спам"])
legal_count = len(df[df[COL_SENT] == "Legal Risk"])
esc_count   = len(df[df[COL_OFFICE].str.contains("ГО", na=False)])

c1.metric("Всего тикетов",        total)
c2.metric("VIP + Priority",       vip_count)
c3.metric("🚨 Спам",              spam_count)
c4.metric("⚖️ Legal Risk",        legal_count)
c5.metric("🔼 Эскалировано в ГО", esc_count)

# ─── ГРАФИКИ ──────────────────────────────────────────────────────────────────
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
    mgr_df = df[df[COL_MANAGER] != "Не найден"]
    if not mgr_df.empty:
        st.bar_chart(mgr_df[COL_MANAGER].value_counts().head(10))

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
    if COL_OFFICE in idx and "ГО" in str(row[COL_OFFICE]):
        styles[idx.index(COL_OFFICE)] = "color: #e67e22; font-weight: bold"
    return styles

show_cols = [c for c in [
    COL_SEG, COL_TYPE, COL_SENT, COL_LANG,
    COL_PRIO, "Приоритет_уровень", COL_SUMMARY,
    COL_MANAGER, COL_ROLE, COL_OFFICE
] if c in fdf.columns]

st.dataframe(
    fdf[show_cols].style.apply(highlight_row, axis=1),
    width='stretch',
    height=450
)
st.caption(f"Показано {len(fdf)} из {total} тикетов")

esc_df = df[df[COL_OFFICE].str.contains("ГО", na=False)]
if not esc_df.empty:
    with st.expander(f"🔼 Эскалированные тикеты ({len(esc_df)} шт) — нажмите для просмотра"):
        esc_cols = [c for c in [COL_SEG, COL_TYPE, COL_PRIO, COL_MANAGER, COL_OFFICE]
                    if c in esc_df.columns]
        st.dataframe(esc_df[esc_cols], width='stretch')

# ─── STAR TASK: AI АССИСТЕНТ ──────────────────────────────────────────────────
st.markdown("---")
st.subheader("🤖 AI-Ассистент (Star Task)")
st.markdown("Задайте вопрос по данным на естественном языке. Ассистент построит анализ и при необходимости сгенерирует график.")

if "chat_history" not in st.session_state:
    st.session_state.chat_history = []

for msg in st.session_state.chat_history:
    with st.chat_message(msg["role"]):
        st.markdown(msg["content"])

user_input = st.chat_input("Например: Покажи распределение типов обращений по офисам")

if user_input:
    st.session_state.chat_history.append({"role": "user", "content": user_input})
    with st.chat_message("user"):
        st.markdown(user_input)

    data_context = f"""
Датасет FIRE Dashboard: {total} тикетов.
Столбцы: {', '.join(df.columns.tolist())}
Уникальные типы обращений: {df[COL_TYPE].value_counts().to_dict()}
Тональности: {df[COL_SENT].value_counts().to_dict()}
Офисы назначения: {df[COL_OFFICE].value_counts().to_dict()}
Сегменты: {df[COL_SEG].value_counts().to_dict()}
Уровни приоритета: {df["Приоритет_уровень"].value_counts().to_dict()}
Менеджеры (топ-5): {df[df[COL_MANAGER] != 'Не найден'][COL_MANAGER].value_counts().head(5).to_dict()}
""".strip()

    system_prompt = f"""Ты — аналитический AI-ассистент дашборда FIRE (Freedom Intelligent Routing Engine).
Ты помогаешь операторам анализировать данные по тикетам клиентов.
Отвечай кратко и по делу на русском языке.

ДАННЫЕ ПО ДАТАСЕТУ:
{data_context}

Если вопрос про графики/визуализацию — опиши выводы словами (у тебя нет доступа к Matplotlib, но дашборд уже показывает графики выше).
Если вопрос — аналитический — дай конкретный ответ с цифрами из датасета."""

    try:
        gemini_api_key = os.getenv("GEMINI_API_KEY", "")
        if not gemini_api_key:
            answer = "⚠️ GEMINI_API_KEY не найден. Добавьте ключ в файл .env"
        else:
            client = genai.Client(api_key=gemini_api_key)

            # Собираем историю
            history_contents = []
            for m in st.session_state.chat_history[:-1]:
                role = "user" if m["role"] == "user" else "model"
                history_contents.append(types.Content(role=role, parts=[types.Part(text=m["content"])]))

            history_contents.append(
                types.Content(role="user", parts=[types.Part(text=f"{system_prompt}\n\nВопрос: {user_input}")])
            )

            response = client.models.generate_content(
                model="gemma-3-27b-it",
                contents=history_contents,
                config=types.GenerateContentConfig(temperature=0.3, max_output_tokens=1024),
            )
            answer = response.text
    except Exception as e:
        answer = f"⚠️ Ошибка AI-ассистента: {str(e)}"

    st.session_state.chat_history.append({"role": "assistant", "content": answer})
    with st.chat_message("assistant"):
        st.markdown(answer)

with st.expander("💡 Примеры вопросов к ассистенту"):
    st.markdown("""
- Сколько VIP-клиентов было эскалировано в главный офис?
- Какой тип обращений встречается чаще всего?
- Покажи статистику по Legal Risk тикетам
- Какой менеджер получил больше всего тикетов?
- Какой процент тикетов получил приоритет High?
    """)