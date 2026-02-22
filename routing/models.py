from django.db import models

class BusinessUnit(models.Model):
    name = models.CharField(max_length=255)
    address = models.TextField()

class Manager(models.Model):
    full_name = models.CharField(max_length=255)
    position = models.CharField(max_length=255)
    skills = models.TextField()
    current_load = models.IntegerField(default=0)
    office = models.ForeignKey(BusinessUnit, on_delete=models.CASCADE)

class Ticket(models.Model):
    # Точная копия колонок из tickets.csv
    guid = models.CharField(max_length=255, unique=True, verbose_name="GUID клиента")
    gender = models.CharField(max_length=50, null=True, blank=True, verbose_name="Пол клиента")
    
    # Дату оставим строкой (CharField), чтобы избежать ошибок парсинга форматов ДД.ММ.ГГГГ
    birth_date = models.CharField(max_length=50, null=True, blank=True, verbose_name="Дата рождения") 
    
    description = models.TextField(verbose_name="Описание")
    attachments = models.TextField(null=True, blank=True, verbose_name="Вложения")
    segment = models.CharField(max_length=100, null=True, blank=True, verbose_name="Сегмент клиента")
    
    # Разделенный адрес
    country = models.CharField(max_length=100, null=True, blank=True, verbose_name="Страна")
    region = models.CharField(max_length=100, null=True, blank=True, verbose_name="Область")
    city = models.CharField(max_length=100, null=True, blank=True, verbose_name="Населённый пункт")
    street = models.CharField(max_length=255, null=True, blank=True, verbose_name="Улица")
    house = models.CharField(max_length=50, null=True, blank=True, verbose_name="Дом")

    def __str__(self):
        return str(self.guid)

class RoutingResult(models.Model):
    ticket = models.OneToOneField(Ticket, on_delete=models.CASCADE, related_name='ai_result', verbose_name="Тикет")

    # Колонки из results.csv — порядок соответствует файлу
    ai_segment            = models.CharField(max_length=100, null=True, blank=True, verbose_name="Сегмент")
    ai_type               = models.CharField(max_length=255, null=True, blank=True, verbose_name="Тип")
    ai_sentiment          = models.CharField(max_length=100, null=True, blank=True, verbose_name="Тональность")
    ai_language           = models.CharField(max_length=50,  null=True, blank=True, verbose_name="Язык")
    ai_priority           = models.CharField(max_length=50,  null=True, blank=True, verbose_name="Приоритет")
    manager_recommendations = models.TextField(null=True, blank=True, verbose_name="Рекомендации менеджеру")
    ai_attachments        = models.TextField(null=True, blank=True, verbose_name="Вложения")
    manager_name          = models.CharField(max_length=255, null=True, blank=True, verbose_name="Назначенный Менеджер")
    manager_position      = models.CharField(max_length=255, null=True, blank=True, verbose_name="Должность")
    ai_assigned_office    = models.CharField(max_length=255, null=True, blank=True, verbose_name="Офис Назначения")
    is_escalated          = models.BooleanField(default=False, verbose_name="Эскалирован")
    city_original         = models.CharField(max_length=255, null=True, blank=True, verbose_name="Город_оригинал")
    routing_reason        = models.TextField(null=True, blank=True, verbose_name="Причина_роутинга")
    ai_source             = models.CharField(max_length=100, null=True, blank=True, verbose_name="AI_Источник")
    geo_method            = models.CharField(max_length=100, null=True, blank=True, verbose_name="Метод_гео")

    # FK-связь с менеджером в БД (опциональная)
    assigned_manager = models.ForeignKey(Manager, on_delete=models.SET_NULL, null=True, blank=True, verbose_name="FK Менеджер")

    def __str__(self):
        return f"Результат ИИ для {self.ticket.guid}"