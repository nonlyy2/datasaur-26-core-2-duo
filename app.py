import streamlit as st
import pandas as pd
import os

st.set_page_config(page_title="FIRE Dashboard", layout="wide")

st.title("🔥 FIRE (Freedom Intelligent Routing Engine)")
st.markdown("Система автоматического распределения обращений клиентов")

data_path = "data/results.csv"

if not os.path.exists(data_path):
    st.warning("Файл с результатами не найден. Сначала запустите Go-движок.")
else:
    # Загружаем данные
    df = pd.read_csv(data_path)
    
    # --- МЕТРИКИ ---
    st.subheader("📊 Оперативная сводка")
    col1, col2, col3, col4 = st.columns(4)
    col1.metric("Всего обработано", len(df))
    col2.metric("VIP Клиентов", len(df[df['Сегмент'] == 'VIP']))
    col3.metric("Выявлено Спама", len(df[df['AI_Тип'] == 'Спам']))
    col4.metric("Legal Risk (Угроза судом)", len(df[df['AI_Тональность'] == 'Legal Risk']))

    # --- ГРАФИКИ ---
    st.markdown("---")
    col1, col2 = st.columns(2)
    
    with col1:
        st.subheader("Типы обращений")
        type_counts = df['AI_Тип'].value_counts()
        st.bar_chart(type_counts)
        
    with col2:
        st.subheader("Нагрузка по городам (Куда ушли тикеты)")
        city_counts = df['Город'].value_counts()
        st.bar_chart(city_counts)

    # --- ТАБЛИЦА ДАННЫХ ---
    st.markdown("---")
    st.subheader("📋 Детализация распределения")
    
    # Красивая подсветка для таблицы
    def highlight_priority(val):
        color = 'red' if val == 'High' else 'orange' if val == 'Medium' else 'green'
        return f'color: {color}'
    
    st.dataframe(df.style.map(highlight_priority, subset=['AI_Приоритет']), use_container_width=True)

    # Заготовка под Star Task
    st.markdown("---")
    st.subheader("🤖 ИИ-Ассистент (Star Task)")
    st.info("Здесь скоро появится чат, где можно будет спросить ИИ: 'Выведи всех недовольных клиентов из Астаны'")