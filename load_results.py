import os
import sys
import io

# ─── ФОРСИРУЕМ UTF-8 ДЛЯ КОНСОЛИ ───────────────────────────────────────────
if sys.stdout.encoding != 'utf-8':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding='utf-8')
# ───────────────────────────────────────────────────────────────────────────

import django
import pandas as pd

BASE_DIR = os.path.dirname(os.path.abspath(__file__))
if BASE_DIR not in sys.path:
    sys.path.insert(0, BASE_DIR)

os.environ.setdefault('DJANGO_SETTINGS_MODULE', 'fire_project.settings')
django.setup()

from routing.models import Ticket, Manager, RoutingResult
from django.db.models import Q

def clean_text(val):
    if pd.isna(val):
        return ""
    return str(val).strip()

def load_results():
    print("📥 Начинаем загрузку новых результатов ИИ...")
    
    try:
        csv_path = next(
            (p for p in [
                os.path.join(BASE_DIR, 'data', 'results.csv'),
                os.path.join(BASE_DIR, 'results.csv'),
            ] if os.path.exists(p)),
            os.path.join(BASE_DIR, 'data', 'results.csv')
        )
        print(f"📂 Читаем: {csv_path}")
        df = pd.read_csv(csv_path, encoding='utf-8-sig', sep=',')
        
        created_count = 0
        updated_count = 0

        for _, row in df.iterrows():
            guid = clean_text(row.get('GUID'))
            if not guid:
                continue
                
            ticket = Ticket.objects.filter(guid=guid).first()
            if not ticket:
                print(f"⚠️ Тикет {guid} не найден. Пропускаем.")
                continue
                
            manager_name = clean_text(row.get('Назначенный Менеджер'))
            new_manager = None
            if manager_name and manager_name not in ['Не найден', '-']:  
                new_manager = Manager.objects.filter(full_name__icontains=manager_name).first()

            # Обновляем или создаем запись
            result, created = RoutingResult.objects.update_or_create(
                ticket=ticket,
                defaults={
                    'ai_segment':             clean_text(row.get('Сегмент')),
                    'ai_type':                clean_text(row.get('Тип')),
                    'ai_sentiment':           clean_text(row.get('Тональность')),
                    'ai_language':            clean_text(row.get('Язык')),
                    'ai_priority':            clean_text(row.get('Приоритет')),
                    'manager_recommendations':clean_text(row.get('Рекомендации менеджеру')),
                    'ai_attachments':         clean_text(row.get('Вложения')),
                    'manager_name':           manager_name,
                    'manager_position':       clean_text(row.get('Должность')),
                    'ai_assigned_office':     clean_text(row.get('Офис Назначения')),
                    'is_escalated':           clean_text(row.get('Эскалирован')) == 'Да',
                    'city_original':          clean_text(row.get('Город_оригинал')),
                    'routing_reason':         clean_text(row.get('Причина_роутинга')),
                    'ai_source':              clean_text(row.get('AI_Источник')),
                    'geo_method':             clean_text(row.get('Метод_гео')),
                    'assigned_manager':       new_manager,
                }
            )
            
            if created:
                created_count += 1
            else:
                updated_count += 1

        print(f"✅ Готово! Создано: {created_count}, Обновлено: {updated_count}")

        # Прибавляем AI-тикеты к текущему значению в PostgreSQL
        print("🔄 Обновляем нагрузку менеджеров...")
        for m in Manager.objects.all():
            m.refresh_from_db()  # читаем актуальное значение из Postgres
            old_load = m.current_load

            # Считаем по FK
            fk_count = RoutingResult.objects.filter(assigned_manager=m).count()
            # Считаем по текстовому совпадению (fallback)
            name_count = RoutingResult.objects.filter(manager_name=m.full_name).count()
            # Итого без дублей
            ai_count = RoutingResult.objects.filter(
                Q(assigned_manager=m) | Q(manager_name=m.full_name)
            ).distinct().count()

            print(f"  [{m.full_name}]")
            print(f"    📖 old_load из БД = {old_load}")
            print(f"    🔗 по FK          = {fk_count}")
            print(f"    📝 по manager_name= {name_count}")
            print(f"    ✅ итого (distinct)= {ai_count}")
            print(f"    💾 new_load       = {old_load} + {ai_count} = {old_load + ai_count}")

            m.current_load = old_load + ai_count
            m.save()
        print("✅ Нагрузка успешно обновлена!")

    except Exception as e:
        print(f"❌ Ошибка: {e}")

if __name__ == '__main__':
    load_results()