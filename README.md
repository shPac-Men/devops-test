# Локальный мониторинг Uptime Kuma

Тестовое Intern DevOps Safeboard 2026: Uptime Kuma с внешней MariaDB и отдельный слой Prometheus/Grafana. Dagger на Go управляет хостовым Docker через Unix socket. Реализованы обязательные команды, общий запуск и два сценария Chaos Testing. Galera — отдельный следующий этап, описанный в [docs/GALERA.md](docs/GALERA.md).

## Требования и подготовка

- Linux с Docker Engine либо Docker Desktop; Windows — WSL2 с Docker integration. Bash и Python 3 нужны для скриптов.
- Docker Compose v2.39.2+, **Dagger CLI 0.21.11**. Локальный Go нужен только для разработки; сборка выполняется внутри Dagger.
- Ориентир: 4 CPU, 8 ГБ RAM, 15 ГБ свободного диска с учётом Dagger и образов. Первый запуск требует доступа к Docker Hub, GHCR, registry.dagger.io.
- `/var/run/docker.sock` должен вести к тому же демону, который виден через `docker info`. Нестандартный rootless-сокет внутри контроллера не поддерживается.
- Свободные локальные порты 3000, 3001, 9090. Выполняйте операции последовательно из корня проекта.

Установите CLI из [официального релиза Dagger 0.21.11](https://github.com/dagger/dagger/releases/tag/v0.21.11): выберите архив своей ОС/архитектуры, проверьте `checksums.txt`, распакуйте бинарник в PATH либо `.tools/dagger`. Версия engine закреплена в `dagger.json`.

```bash
docker info
docker compose version
dagger version  # либо .tools/dagger version
python3 scripts/init-secrets.py
```

Скрипт создаёт четыре случайных пароля по 256 бит в `.secrets/` и сохраняет существующие файлы. Не удаляйте их после первого запуска: пароли уже используются сохранёнными БД. Ротация не автоматизирована; другие значения будут отклонены.

При необходимости скопируйте `.env.example` в `.env` и измените опубликованные порты. `DB_HOST` — адрес БД внутри Docker; для базовой версии оставьте `mariadb`. В `.env` храните только настройки, секреты находятся отдельно.

## Запуск

```bash
bash scripts/dagger.sh deploy-all
```

Обёртка проверяет версию CLI и передаёт сокет/секреты в Dagger. Эквивалентный полный вызов:

```bash
dagger call \
  --docker-socket=/var/run/docker.sock \
  --root-password=file://.secrets/root-password \
  --kuma-password=file://.secrets/kuma-password \
  --exporter-password=file://.secrets/exporter-password \
  --grafana-password=file://.secrets/grafana-password \
  deploy-all
```

Общие аргументы конструктора нужны всем командам. При нестандартных портах передайте также `--kuma-port`, `--grafana-port`, `--prometheus-port` перед именем функции. Имена `dagger call deploy-app` и аналогичные обозначают функции; доступ к хосту требует явных аргументов выше.

Раздельный запуск:

```bash
bash scripts/dagger.sh deploy-app
bash scripts/dagger.sh deploy-monitoring
bash scripts/dagger.sh configure-grafana
bash scripts/dagger.sh verify
```

`deploy-monitoring` можно запустить первым: отсутствие приложения отобразится как ошибка. `deploy-all` запускает оба слоя, настраивает Grafana и проверяет контейнеры, семь scrape targets, ресурсы и общий статус через Grafana API. Повторный вызов сохраняет данные и обновляет объекты Grafana с теми же UID. Загрузка образов может занять несколько минут; тайм-аут готовности сервисов — 180 секунд.

| Интерфейс | Адрес по умолчанию | Вход |
|---|---|---|
| Uptime Kuma | [localhost:3001](http://localhost:3001) | Создать администратора при первом входе |
| Grafana | [localhost:3000](http://localhost:3000) | `admin`, пароль из `.secrets/grafana-password` |
| Дашборд | [Safeboard](http://localhost:3000/d/safeboard-health) | Учётная запись Grafana |
| Prometheus | [localhost:9090](http://localhost:9090) | Только loopback, без аутентификации |

Прочитать пароль Grafana локально: `cat .secrets/grafana-password`. Не прикладывайте его к отчётам.

В Kuma создайте администратора и добавьте HTTP-монитор `http://grafana:3000/api/health` и TCP-монитор `mariadb:3306`, например с интервалом 20 секунд. Подключение самой Kuma к MariaDB уже настроено. Kuma проверяет независимый blackbox_exporter, поэтому отказ приложения не останавливает наблюдение за ним.

## Архитектура

```text
Dagger Go -> Docker CLI + forwarded Unix socket
                  |
                  +-> temporary controller in network safeboard
                         |-- docker cp -> config/secret volumes
                         |-- Compose safeboard-app: MariaDB <- Kuma
                         |-- Compose safeboard-monitoring:
                         |      exporters -> Prometheus <- Grafana
                         +-- Grafana API and acceptance checks
```

Контроллер из `cmd/platform` собирается без CGO и запускается во временном контейнере `docker:28.5.1-cli`. После команды контейнер удаляется. Конфигурации передаются через `docker cp`: пути внутри Dagger не используются как bind mounts хостового демона. Функции помечены `cache="never"`; уникальный RUN_ID предотвращает кэширование слоя с внешними эффектами.

Именованные volumes `safeboard-app_mariadb-data`, `safeboard-app_kuma-data`, `safeboard-monitoring_prometheus-data`, `safeboard-monitoring_grafana-data` сохраняются при пересоздании контейнеров. Prometheus хранит семь дней истории. Секреты разделены по volumes: Kuma не получает root-пароль. MariaDB читает секрет экспортёра для первичной инициализации. Grafana конфигурируется через API, JSON дашборда хранится в репозитории.

cAdvisor собирает ресурсы контейнеров; node_exporter — ресурсы **хоста Docker**. На Docker Desktop это Linux VM. cAdvisor использует privileged и системные read-only mounts. Контроллер и cAdvisor требуют доверенного локального Docker. БД и экспортёры не публикуют порты.

Сбор метрик и health rules — каждые 5 секунд. Проверяются HTTP probes, `mysql_up`, `up` и свежесть данных. Отсутствующая серия считается отказом; при недоступном Prometheus Grafana показывает «Нет данных». Статусные панели используют instant queries. Стенд восстанавливается после сбоя контейнера, но не обеспечивает доступность при потере единственного хоста или Docker-демона.

## Chaos Testing

```bash
bash scripts/dagger.sh chaos-test --target=kuma --mode=both
bash scripts/dagger.sh chaos-test --target=mariadb --mode=both
```

`--target`: `kuma`/`mariadb`; `--mode`: `stop`/`crash`/`both`. Перед тестом все системы должны быть здоровы.

1. **stop:** выполняет `docker stop`, ждёт статуса 0 через datasource Grafana, записывает annotation, держит отказ ещё 15 секунд. Затем выполняет `docker start` и ждёт статуса 1.
2. **crash:** после минимум 10 секунд uptime отправляет SIGKILL init-процессу контейнера через временный контейнер с `--pid=host --cap-drop=ALL --cap-add=KILL`. PID берётся из Docker inspect и проверяется. Это имитирует сбой процесса без kill API. Тест проверяет увеличение `RestartCount`, готовность сервиса и статус 1. Быстрый сбой может попасть между scrape-интервалами; автоматический рестарт подтверждается счётчиком, длительный видимый отказ — первым сценарием.

Откройте дашборд с обновлением 5 секунд: история и annotations показывают события теста. При ошибке или штатной отмене контроллер восстанавливает сервис в отдельном ограниченном контексте. При принудительном завершении Docker/машины повторите `deploy-all`.

После ручной остановки Docker подавляет restart-policy до явного запуска контейнера или соответствующего перезапуска демона. В проверенном Docker 29.8.1 это относится и к `docker kill --signal=SIGKILL`: контейнер завершался с кодом 137 и RestartCount=0. Поэтому crash использует прямой сигнал процессу, а stop/start остаётся отдельной проверкой длительного отказа. [Документация Docker](https://docs.docker.com/engine/containers/start-containers-automatically/).

## Проверки и обслуживание

```bash
go test -race ./...
go vet ./...
docker compose -f compose.app.yaml config --quiet
docker compose -f compose.monitoring.yaml config --quiet
bash scripts/dagger.sh verify
```

`deploy-monitoring` запускает `promtool check config` вместе с rules. После изменения дашборда выполните `python3 scripts/dashboard.py` и `configure-grafana`. После изменения Prometheus/blackbox выполните `deploy-monitoring`: команда перезапускает их для загрузки конфигураций.

Фактические результаты локальных проверок и их границы приведены в [docs/VALIDATION.md](docs/VALIDATION.md). `AI_REVIEW.md` содержит использованные запросы и пять обнаруженных ошибок AI с исправлениями.

Остановка с сохранением данных:

```bash
docker compose -f compose.app.yaml down
docker compose -f compose.monitoring.yaml down
```

Не используйте `down -v`, если данные нужны. Для проверки persistence создайте аккаунт/монитор Kuma, выполните `down`, затем `deploy-all` и убедитесь, что они сохранились. Перед ручным удалением volumes сделайте backup.

| Симптом | Действие |
|---|---|
| Permission denied Docker socket | Проверить `docker info`, настройки Docker/WSL и права пользователя; не делать сокет общедоступным |
| Port already allocated | Освободить порт или изменить его в `.env`; повторить deploy |
| Secret differs from existing volume | Восстановить первоначальные `.secrets` |
| MariaDB unhealthy | Проверить диск, прежние volumes и локально `docker compose -f compose.app.yaml logs --tail=80 mariadb` |
| Нет метрик контейнеров | Проверить cAdvisor logs, cgroup v2, системные mounts и Docker API; `verify` не принимает пустые метрики |
| Grafana HTTP 401 | Восстановить исходный пароль; ручная смена в UI требует согласованной ротации |
| Scrape target DOWN | Проверить [targets](http://localhost:9090/targets), Docker DNS и экспортёр |

Логи приложений могут содержать чувствительные сведения: просматривайте их локально и проверяйте перед отправкой. Контроллер выводит статусы контейнеров без значений секретов.

## Сдача

```bash
python3 scripts/package.py --surname Фамилия --name Имя
```

Архив `dist/Фамилия_Имя_DevOps_Тестовое.tar.gz` включает только материалы запуска и проверки. Секреты, рабочие данные, `.git`, Dagger CLI, исходное задание и вложенный пустой репозиторий исключены. Получатель создаёт собственные секреты. Перед сдачей распакуйте архив и повторите запуск. Загрузку на портал кандидат выполняет самостоятельно.
