from django.db import migrations, models


class Migration(migrations.Migration):

    dependencies = [
        ('routing', '0004_routingresult_ai_assigned_office_and_more'),
    ]

    operations = [
        migrations.AddField(
            model_name='routingresult',
            name='manager_name',
            field=models.CharField(blank=True, max_length=255, null=True, verbose_name='Назначенный Менеджер'),
        ),
        migrations.AddField(
            model_name='routingresult',
            name='manager_position',
            field=models.CharField(blank=True, max_length=255, null=True, verbose_name='Должность'),
        ),
        migrations.AddField(
            model_name='routingresult',
            name='is_escalated',
            field=models.BooleanField(default=False, verbose_name='Эскалирован'),
        ),
        migrations.AddField(
            model_name='routingresult',
            name='city_original',
            field=models.CharField(blank=True, max_length=255, null=True, verbose_name='Город_оригинал'),
        ),
        migrations.AddField(
            model_name='routingresult',
            name='routing_reason',
            field=models.TextField(blank=True, null=True, verbose_name='Причина_роутинга'),
        ),
        migrations.AddField(
            model_name='routingresult',
            name='ai_source',
            field=models.CharField(blank=True, max_length=100, null=True, verbose_name='AI_Источник'),
        ),
        migrations.AddField(
            model_name='routingresult',
            name='geo_method',
            field=models.CharField(blank=True, max_length=100, null=True, verbose_name='Метод_гео'),
        ),
        migrations.AlterField(
            model_name='routingresult',
            name='ai_segment',
            field=models.CharField(blank=True, max_length=100, null=True, verbose_name='Сегмент'),
        ),
    ]
