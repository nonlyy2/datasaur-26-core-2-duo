import os
import django
import pandas as pd

BASE_DIR = os.path.dirname(os.path.abspath(__file__))
os.environ.setdefault('DJANGO_SETTINGS_MODULE', 'fire_project.settings')
django.setup()

from routing.models import BusinessUnit, Manager, Ticket

def clean_text(val):
    if pd.isna(val):
        return ""
    return str(val).strip()

def safe_int(val):
    try:
        if pd.isna(val) or val == "" or val == " ":
            return 0
        return int(float(val))
    except (ValueError, TypeError):
        return 0

def load_all():
    # 1. Загрузка Офисов (ОБЯЗАТЕЛЬНО до менеджеров — FK зависимость)
    try:
        path = os.path.join(BASE_DIR, 'data', 'business_units.csv')
        df_offices = pd.read_csv(path, encoding='utf-8-sig', sep=',')
        df_offices.columns = df_offices.columns.str.strip()
        count = 0
        for _, row in df_offices.iterrows():
            name = clean_text(row.get('Офис'))
            if name:
                BusinessUnit.objects.update_or_create(
                    name=name,
                    defaults={'address': clean_text(row.get('Адрес', ''))}
                )
                count += 1
        print(f"✅ Офисов загружено: {count}")
    except Exception as e:
        print(f"❌ Ошибка офисов: {e}")

    # 2. Загрузка Менеджеров
    try:
        path = os.path.join(BASE_DIR, 'data', 'managers.csv')
        df_managers = pd.read_csv(path, encoding='utf-8-sig', sep=',')
        df_managers.columns = df_managers.columns.str.strip()

        count = 0
        for _, row in df_managers.iterrows():
            full_name = clean_text(row.get('ФИО'))
            if full_name:
                office_name = clean_text(row.get('Офис'))
                office = BusinessUnit.objects.filter(name__icontains=office_name).first()
                Manager.objects.update_or_create(
                    full_name=full_name,
                    defaults={
                        'position':     clean_text(row.get('Должность')),
                        'skills':       clean_text(row.get('Навыки')),
                        'office':       office,
                        'current_load': safe_int(row.get('Количество обращений в работе'))
                    }
                )
                count += 1
        print(f"✅ Менеджеров загружено: {count}")
    except Exception as e:
        print(f"❌ Ошибка менеджеров: {e}")

    # 3. Загрузка Тикетов
    try:
        path = os.path.join(BASE_DIR, 'data', 'tickets.csv')
        df_tickets = pd.read_csv(path, encoding='utf-8-sig', sep=',')
        df_tickets.columns = df_tickets.columns.str.strip()

        count = 0
        for _, row in df_tickets.iterrows():
            guid = clean_text(row.get('GUID клиента'))
            if guid:
                city_val = row.get('Населённый пункт')
                if pd.isna(city_val):
                    city_val = row.get('Населенный пункт')
                Ticket.objects.update_or_create(
                    guid=guid,
                    defaults={
                        'gender':      clean_text(row.get('Пол клиента')),
                        'birth_date':  clean_text(row.get('Дата рождения')),
                        'description': clean_text(row.get('Описание')),
                        'attachments': clean_text(row.get('Вложения')),
                        'segment':     clean_text(row.get('Сегмент клиента')),
                        'country':     clean_text(row.get('Страна')),
                        'region':      clean_text(row.get('Область')),
                        'city':        clean_text(city_val),
                        'street':      clean_text(row.get('Улица')),
                        'house':       clean_text(row.get('Дом'))
                    }
                )
                count += 1
        print(f"✅ Тикетов загружено: {count}")
    except Exception as e:
        print(f"❌ Ошибка тикетов: {e}")

if __name__ == '__main__':
    load_all()
