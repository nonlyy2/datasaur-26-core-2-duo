"""
reset_all.py — Полный сброс системы FIRE
=========================================
Что делает:
  1. Удаляет data/results.csv
  2. Дропает и пересоздаёт PostgreSQL-базу
  3. Применяет все миграции (включая новые)
  4. Загружает начальные данные: офисы → менеджеры → тикеты

После этого скрипта запустите анализ через Streamlit или вручную:
  go run main.go
  python load_results.py
"""

import os
import sys
import subprocess

import psycopg2
import pandas as pd
from dotenv import load_dotenv

load_dotenv()

DB_HOST = os.getenv("DB_HOST", "localhost")
DB_PORT = os.getenv("DB_PORT", "5433")
DB_USER = os.getenv("DB_USER", "postgres")
DB_PASS = os.getenv("DB_PASS", "1234")
DB_NAME = os.getenv("DB_NAME", "fire_db")

# Настраиваем Django ДО django.setup() — вызовем его после migrate
os.environ.setdefault("DJANGO_SETTINGS_MODULE", "fire_project.settings")

# ─── Helpers ────────────────────────────────────────────────────────────────

def step(n, msg):
    print(f"\n{'─'*55}")
    print(f"  {n}  {msg}")
    print(f"{'─'*55}")

def clean(val):
    if pd.isna(val):
        return ""
    return str(val).strip()

def safe_int(val):
    try:
        if pd.isna(val) or str(val).strip() == "":
            return 0
        return int(float(val))
    except (ValueError, TypeError):
        return 0

# ─── Шаги ───────────────────────────────────────────────────────────────────

def delete_results_csv():
    step("1/4", "Удаление results.csv")
    deleted = False
    for path in ["data/results.csv", "results.csv"]:
        if os.path.exists(path):
            os.remove(path)
            print(f"  ✅ Удалён: {path}")
            deleted = True
    if not deleted:
        print("  ℹ️  results.csv не найден — пропускаем")


def recreate_database():
    step("2/4", f"Пересоздание БД  '{DB_NAME}'  на {DB_HOST}:{DB_PORT}")
    try:
        conn = psycopg2.connect(
            host=DB_HOST, port=DB_PORT,
            user=DB_USER, password=DB_PASS,
            dbname="postgres"          # подключаемся к системной БД, не к fire_db
        )
        conn.autocommit = True
        cur = conn.cursor()

        # Закрываем все открытые соединения к целевой БД
        cur.execute(f"""
            SELECT pg_terminate_backend(pid)
            FROM pg_stat_activity
            WHERE datname = %s AND pid <> pg_backend_pid()
        """, (DB_NAME,))

        cur.execute(f'DROP DATABASE IF EXISTS "{DB_NAME}"')
        print(f"  ✅ База '{DB_NAME}' удалена")

        cur.execute(f'CREATE DATABASE "{DB_NAME}" ENCODING \'UTF8\'')
        print(f"  ✅ База '{DB_NAME}' создана заново")

        cur.close()
        conn.close()
    except Exception as e:
        print(f"  ❌ Ошибка при работе с PostgreSQL: {e}")
        sys.exit(1)


def run_migrations():
    step("3/4", "Django migrate  (применяем все миграции)")
    result = subprocess.run(
        ["python", "manage.py", "migrate"],
        capture_output=False   # вывод сразу в консоль
    )
    if result.returncode != 0:
        print("  ❌ migrate завершился с ошибкой")
        sys.exit(1)
    print("  ✅ Все миграции применены")


def load_initial_data():
    step("4/4", "Загрузка начальных данных: офисы → менеджеры → тикеты")

    import django
    django.setup()
    from routing.models import BusinessUnit, Manager, Ticket

    # ── 4a. Офисы ─────────────────────────────────────────────────────────
    print("\n  ► Офисы (data/business_units.csv)...")
    try:
        df = pd.read_csv("data/business_units.csv", encoding="utf-8-sig")
        df.columns = df.columns.str.strip()
        count = 0
        for _, row in df.iterrows():
            name = clean(row.get("Офис"))
            if name:
                BusinessUnit.objects.update_or_create(
                    name=name,
                    defaults={"address": clean(row.get("Адрес", ""))}
                )
                count += 1
        print(f"  ✅ Офисов загружено: {count}")
    except Exception as e:
        print(f"  ❌ Ошибка при загрузке офисов: {e}")
        sys.exit(1)

    # ── 4b. Менеджеры ─────────────────────────────────────────────────────
    print("\n  ► Менеджеры (data/managers.csv)...")
    try:
        df = pd.read_csv("data/managers.csv", encoding="utf-8-sig")
        df.columns = df.columns.str.strip()
        count = 0
        for _, row in df.iterrows():
            full_name = clean(row.get("ФИО"))
            if not full_name:
                continue
            office_name = clean(row.get("Офис", ""))
            office = BusinessUnit.objects.filter(name__icontains=office_name).first() if office_name else None
            if office is None:
                print(f"    ⚠️  Офис не найден для менеджера '{full_name}' (офис: '{office_name}')")
                continue
            Manager.objects.update_or_create(
                full_name=full_name,
                defaults={
                    "position":     clean(row.get("Должность", "")),
                    "skills":       clean(row.get("Навыки", "")),
                    "office":       office,
                    "current_load": safe_int(row.get("Количество обращений в работе", 0)),
                }
            )
            count += 1
        print(f"  ✅ Менеджеров загружено: {count}")
    except Exception as e:
        print(f"  ❌ Ошибка при загрузке менеджеров: {e}")
        sys.exit(1)

    # ── 4c. Тикеты ────────────────────────────────────────────────────────
    print("\n  ► Тикеты (data/tickets.csv)...")
    try:
        df = pd.read_csv("data/tickets.csv", encoding="utf-8-sig")
        df.columns = df.columns.str.strip()
        count = 0
        for _, row in df.iterrows():
            guid = clean(row.get("GUID клиента"))
            if not guid:
                continue
            # Поддержка обоих написаний буквы ё
            city = row.get("Населённый пункт")
            if pd.isna(city):
                city = row.get("Населенный пункт")
            Ticket.objects.update_or_create(
                guid=guid,
                defaults={
                    "gender":      clean(row.get("Пол клиента")),
                    "birth_date":  clean(row.get("Дата рождения")),
                    "description": clean(row.get("Описание")),
                    "attachments": clean(row.get("Вложения")),
                    "segment":     clean(row.get("Сегмент клиента")),
                    "country":     clean(row.get("Страна")),
                    "region":      clean(row.get("Область")),
                    "city":        clean(city),
                    "street":      clean(row.get("Улица")),
                    "house":       clean(row.get("Дом")),
                }
            )
            count += 1
        print(f"  ✅ Тикетов загружено: {count}")
    except Exception as e:
        print(f"  ❌ Ошибка при загрузке тикетов: {e}")
        sys.exit(1)


# ─── Main ────────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    print("\n🔥 FIRE — Полный сброс и чистая инициализация системы")
    print("=" * 55)
    print("⚠️  Это удалит ВСЕ данные из БД и results.csv.\n")

    answer = input("Продолжить? (y/N): ").strip().lower()
    if answer != "y":
        print("Отменено.")
        sys.exit(0)

    delete_results_csv()
    recreate_database()
    run_migrations()
    load_initial_data()

    print(f"\n{'='*55}")
    print("✅ Система сброшена. Начальные данные загружены.")
    print("""
Следующие шаги:
  • Через Streamlit:  нажмите ▶ Запустить анализ
  • Вручную:
      go run main.go
      python load_results.py
""")
