from django.db import migrations, models


class Migration(migrations.Migration):

    dependencies = [
        ('routing', '0006_alter_routingresult_assigned_manager'),
    ]

    operations = [
        migrations.AddField(
            model_name='routingresult',
            name='ai_attachments',
            field=models.TextField(blank=True, null=True, verbose_name='Вложения'),
        ),
    ]
