# Результаты локальной проверки

Дата: 4 ноября 2026 года. Среда: Linux amd64, Docker Engine 29.8.1, Compose v2.39.2, Dagger CLI/engine v0.21.11. Go-тесты выполнялись Go 1.26.8. Другие ОС и архитектуры не проверялись.

## Выполненные проверки

| Сценарий | Результат |
|---|---|
| Развёртывание приложения на новых volumes | MariaDB и Kuma healthy; Kuma использует внешнюю MariaDB |
| Полный deploy-all | Восемь контейнеров, семь scrape targets, CPU/RAM-метрики и общий статус 1 |
| Go tests с race detector | PASS: создание/обновление Grafana, HTTP 401, отсутствие/старение/некорректные значения метрик, тайм-ауты и отмена |
| go vet | PASS |
| Оба Compose config --quiet | PASS |
| promtool check config и test rules | PASS внутри deploy-monitoring; здоровые, отказавшие, отсутствующие и устаревшие серии |
| Kuma stop/start | Статус через Grafana datasource API 1 → 0 → 1; сохранены annotations |
| Kuma аварийный SIGKILL процесса | RestartCount 0 → 1, сервис healthy, статус 1 |
| MariaDB stop/start | Статус через Grafana datasource API 1 → 0 → 1 |
| MariaDB аварийный SIGKILL процесса | RestartCount 0 → 1, БД и общий статус восстановились |
| Штатная отмена неуспешного Chaos Testing | Восстановлена остановленная Kuma; основной тест вернул ошибку отмены |
| deploy-monitoring при остановленных приложении и БД | Успех; приложение осталось остановленным, общий статус 0 |
| Отсутствующий Docker socket | Ненулевой код, сообщение Docker socket not found |
| Неверный root-secret | Ненулевой код, secret differs from existing Docker volume; БД не изменялась |
| Занятый порт | Ненулевой код, port is already allocated; обычный deploy-app восстановил порт 3001 |
| Запуск распакованного архива вне Git проекта | Полный deploy-all прошёл после исправления defaultPath |
| Пересоздание обоих Compose-слоёв без удаления volumes | Контрольная SQL-запись в БД и hash конфигурации Kuma сохранились |
| Повторная настройка Grafana | В сохранённой Grafana ровно один datasource UID safeboard-prometheus и один dashboard UID safeboard-health |
| Состав архива | Проверены отсутствие .secrets, .git, .tools, исходного DOCX, вложенного репозитория и значений реальных локальных паролей |

Краткие результаты интеграционных команд:

```text
Deploy application: MariaDB -> Kuma
Deploy monitoring (application may be offline)
Configure Grafana through HTTP API
Validate containers, seven scrape targets, resources and Grafana health
OK: deploy-all

CHAOS kuma: docker stop; waiting for red Grafana status
PASS: observed red -> green through Grafana API
CHAOS kuma: SIGKILL; restart count before=0
PASS: automatic restart and healthy Grafana status
OK: chaos-test

CHAOS mariadb: docker stop; waiting for red Grafana status
PASS: observed red -> green through Grafana API
CHAOS mariadb: SIGKILL; restart count before=0
PASS: automatic restart and healthy Grafana status
OK: chaos-test

PASS: MariaDB marker and Kuma config preserved;
exactly one datasource and dashboard; marker removed
```

Архив проверялся из нового каталога без `.git` самого проекта. Контейнеры перед запуском удалялись через Compose down, volumes сохранялись для отдельной проверки persistence. Использовались исходные локальные секреты; другой экземпляр стенда на пустом независимом Docker-демоне не запускался.

Проверка persistence использовала отдельную временную таблицу в БД Kuma. Контрольная таблица удалена после успешной проверки. Первый администратор и мониторы не создавались автоматически: это согласованный шаг через UI. Сохранение созданных пользователем аккаунта/мониторов и визуальный осмотр панелей остаются ручными проверками по README; состояние Grafana подтверждено через её API.

После проверок стенд оставлен работающим: Kuma на 3001, Grafana на 3000, Prometheus на 9090. Значения секретов не входят в этот отчёт или архив.
