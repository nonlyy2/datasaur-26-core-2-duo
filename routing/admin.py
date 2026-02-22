from django.contrib import admin
from .models import BusinessUnit, Manager, Ticket, RoutingResult

@admin.register(BusinessUnit)
class BusinessUnitAdmin(admin.ModelAdmin):
    list_display = ('name', 'address')

@admin.register(Manager)
class ManagerAdmin(admin.ModelAdmin):
    list_display = ('full_name', 'position', 'office', 'skills', 'current_load')
    list_filter = ('office', 'position')
    search_fields = ('full_name',)

# Это позволит видеть результаты ИИ прямо внутри карточки Тикета
class RoutingResultInline(admin.StackedInline):
    model = RoutingResult
    can_delete = False
    verbose_name_plural = 'Результат обработки ИИ'

# (Остальной код сверху остается без изменений)

@admin.register(Ticket)
class TicketAdmin(admin.ModelAdmin):
    # Добавили get_ai_priority в общий список
    list_display = ('guid', 'segment', 'city', 'get_ai_priority', 'get_ai_type', 'get_manager')
    list_filter = ('segment', 'city')
    search_fields = ('guid', 'description')
    inlines = [RoutingResultInline]

    def get_ai_type(self, obj):
        return obj.ai_result.ai_type if hasattr(obj, 'ai_result') else "-"
    get_ai_type.short_description = 'Тип (ИИ)'

    def get_ai_priority(self, obj):
        return obj.ai_result.ai_priority if hasattr(obj, 'ai_result') else "-"
    get_ai_priority.short_description = 'Приоритет'

    def get_manager(self, obj):
        if hasattr(obj, 'ai_result') and obj.ai_result.assigned_manager:
            return obj.ai_result.assigned_manager.full_name
        return "Не назначен"
    get_manager.short_description = 'Менеджер'

@admin.register(RoutingResult)
class RoutingResultAdmin(admin.ModelAdmin):
    # Обновили колонки под новые данные
    list_display = ('ticket', 'ai_type', 'ai_priority', 'ai_sentiment', 'ai_assigned_office', 'assigned_manager')
    list_filter = ('ai_type', 'ai_priority', 'ai_sentiment', 'ai_language', 'ai_assigned_office')
    search_fields = ('ticket__guid', 'manager_recommendations')