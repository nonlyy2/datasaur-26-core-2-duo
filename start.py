"""
start.py — Быстрый запуск FIRE для проверяющих
================================================
Делает всё автоматически:
  1. Создаёт PostgreSQL-базу fire_db (если не существует)
  2. Применяет Django-миграции
  3. Загружает офисы → менеджеры → тикеты из CSV
  4. Запускает Streamlit дашборд

Требования:
  - PostgreSQL-сервер запущен (localhost:5433 или из .env)
  - .env с GEMINI_API_KEY, DB_HOST, DB_PORT, DB_USER, DB_PASS, DB_NAME
  - pip install -r requirements.txt
"""

import os
import sys
import subprocess

# Читаем .env вручную (до django.setup)
try:
    from dotenv import load_dotenv
    load_dotenv()
except ImportError:
    pass

DB_HOST = os.getenv("DB_HOST", "localhost")
DB_PORT = os.getenv("DB_PORT", "5433")
DB_USER = os.getenv("DB_USER", "postgres")
DB_PASS = os.getenv("DB_PASS", "1234")
DB_NAME = os.getenv("DB_NAME", "fire_db")

BASE_DIR = os.path.dirname(os.path.abspath(__file__))

def step(n, msg):
    print(f"\n{'─'*55}")
    print(f"  {n}  {msg}")
    print(f"{'─'*55}")

# ── 1. Создаём БД если не существует ────────────────────────
def ensure_database():
    step("1/3", f"Проверка базы данных '{DB_NAME}'")
    try:
        import psycopg2
        # Подключаемся к системной БД postgres
        conn = psycopg2.connect(
            host=DB_HOST, port=DB_PORT,
            user=DB_USER, password=DB_PASS,
            dbname="postgres"
        )
        conn.autocommit = True
        cur = conn.cursor()

        cur.execute("SELECT 1 FROM pg_database WHERE datname = %s", (DB_NAME,))
        exists = cur.fetchone()

        if exists:
            print(f"  ✅ База '{DB_NAME}' уже существует")
        else:
            cur.execute(f'CREATE DATABASE "{DB_NAME}" ENCODING \'UTF8\'')
            print(f"  ✅ База '{DB_NAME}' создана")

        cur.close()
        conn.close()
    except Exception as e:
        print(f"\n  ❌ Не удалось подключиться к PostgreSQL: {e}")
        print("""
  Убедитесь что:
    • PostgreSQL-сервер запущен
    • Параметры в .env верные (DB_HOST, DB_PORT, DB_USER, DB_PASS)
    • Пользователь имеет права на создание БД
""")
        sys.exit(1)

# ── 2. Миграции ──────────────────────────────────────────────
def run_migrations():
    step("2/3", "Применение Django-миграций")
    result = subprocess.run(
        [sys.executable, "manage.py", "migrate"],
        cwd=BASE_DIR
    )
    if result.returncode != 0:
        print("  ❌ Миграции завершились с ошибкой")
        sys.exit(1)
    print("  ✅ Миграции применены")

# ── 3. Загрузка данных ───────────────────────────────────────
def load_data():
    step("3/3", "Загрузка данных из CSV (офисы → менеджеры → тикеты)")
    result = subprocess.run(
        [sys.executable, "load_data.py"],
        cwd=BASE_DIR
    )
    if result.returncode != 0:
        print("  ❌ Ошибка при загрузке данных")
        sys.exit(1)

# ── 4. Streamlit ─────────────────────────────────────────────
def launch_streamlit():
    print(f"\n{'═'*55}")
    print("  🚀 Запуск Streamlit дашборда...")
    print(f"{'═'*55}\n")
    # Запускаем Streamlit — он перехватывает управление
    os.execvp(sys.executable, [sys.executable, "-m", "streamlit", "run", "app.py"])

# ── Main ─────────────────────────────────────────────────────
if __name__ == "__main__":
    print("\n🔥 FIRE — Freedom Intelligent Routing Engine")
    print("   Автоматический запуск системы\n")

    ensure_database()
    run_migrations()
    load_data()
    launch_streamlit()
