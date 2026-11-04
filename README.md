# Локальный контур Uptime Kuma

Слой приложения находится в `compose.app.yaml`, слой мониторинга — в `compose.monitoring.yaml`. Go-модуль Dagger управляет запуском через хостовый Docker-сокет и применяет источник данных и дашборд Grafana через API.

## Основной запуск через Dagger

Установите Dagger CLI **v0.21.11** и Docker Engine. Из корня репозитория выполните три обязательные команды через обёртку:

```bash
sh scripts/dagger.sh deploy-app
sh scripts/dagger.sh deploy-monitoring
sh scripts/dagger.sh configure-grafana
```

Обёртка использует `dagger` из PATH или локальную `.tools/dagger` и подставляет абсолютный путь проекта и `/var/run/docker.sock`. При необходимости предварительно сгенерировать локальный Go SDK выполните `dagger develop --compat=skip`; локальная установка Go для вызова модуля не нужна.

Эквивалентные прямые вызовы CLI:

```bash
dagger call --project-dir="$PWD" --docker-socket=/var/run/docker.sock deploy-app
dagger call --project-dir="$PWD" --docker-socket=/var/run/docker.sock deploy-monitoring
dagger call --project-dir="$PWD" --docker-socket=/var/run/docker.sock configure-grafana
```

`deploy-app` создаёт недостающие пароли и ждёт готовности MariaDB и Kuma. `deploy-monitoring` требует запущенного слоя приложения, подготавливает пользователя БД, проверяет конфигурацию Prometheus и ждёт готовности шести сервисов. `configure-grafana` требует работающего мониторинга и создаёт или обновляет источник `safeboard-prometheus` и дашборд `safeboard-health`. Повторные вызовы сохраняют существующие пароли и тома; команды развёртывания не кэшируются.

Для последовательного выполнения всех трёх этапов одной командой:

```bash
sh scripts/dagger.sh deploy-all
# Прямой вызов:
dagger call --project-dir="$PWD" --docker-socket=/var/run/docker.sock deploy-all
```

`deploy-all` вызывает существующие функции по порядку и останавливается на первой ошибке с ненулевым кодом выхода. Диагностика содержит номер этапа, имя команды и исходную причину. Уже завершённые этапы автоматически не откатываются; после устранения причины команду можно выполнить повторно. Galera пока не реализована.

## Chaos testing

Сначала выполните `deploy-all`. Затем проверяйте цели по отдельности, без одновременного запуска двух chaos-тестов:

```bash
sh scripts/dagger.sh chaos-test --target=mariadb
sh scripts/dagger.sh chaos-test --target=uptime-kuma
# Прямой вызов, например для MariaDB:
dagger call --project-dir="$PWD" --docker-socket=/var/run/docker.sock chaos-test --target=mariadb
```

Параметр `--timeout=120` задаёт таймаут каждой фазы, допустимы значения 30–600 секунд. Вся команда занимает несколько фаз. Код 0 означает, что все проверки пройдены; при ошибке выводятся цель, неудавшаяся проверка и причина.

Сценарий реализован в `scripts/chaos-test.py` и запускается Dagger в короткоживущем Python-контейнере со стандартной библиотекой. Он находит ровно один контейнер по Compose-меткам проекта и выбранного сервиса; до воздействия требует его готовности, restart-policy и исправного мониторинга.

1. Через Docker Engine stop API (эквивалент `docker stop`) останавливает выбранный контейнер. Через API Grafana запрашивает Prometheus и выражение фактически сохранённой статусной панели дашборда. Проверяет статус 0 на двух разных свежих сборах при работающем экспортёре.
2. Явно запускает контейнер и ждёт Docker healthcheck и свежего статуса 1 на дашборде.
3. После минимум 10 секунд работы завершает основной процесс контейнера сигналом SIGKILL по его PID на Docker-хосте. Проверяет увеличение `RestartCount`, готовность контейнера и восстановление статуса 1 **без вызова start в этой фазе**. Так проверяется сама restart-policy.

Эти сценарии разделены потому, что [Docker отключает автоматический перезапуск после ручной остановки](https://docs.docker.com/engine/containers/start-containers-automatically/#restart-policy-details). Выдавать явный `start` за срабатывание restart-policy было бы неверно. Сигнал основному процессу отправляется напрямую, а не через Docker stop/kill API, которые помечают остановку как ручную; [исходник Docker](https://github.com/moby/moby/blob/master/daemon/kill.go).

Для ручного наблюдения откройте [дашборд](http://127.0.0.1:3000/d/safeboard-health) до запуска теста. Соответствующая панель MariaDB или Uptime Kuma должна показать красную «Проблема», затем зелёную «OK». Проверка API подтверждает данные сохранённой панели, но не делает скриншот интерфейса. Историю отказа можно посмотреть в Prometheus по `mysql_up{job="mariadb"}` или `probe_success{job="kuma-http"}`.

Тест требует rootful Linux Docker: помощник получает хостовые PID/network namespaces и Docker-сокет, проект монтируется только для чтения. Он изменяет состояние выбранного сервиса, не удаляя контейнеры и тома. При ошибке или обрабатываемом SIGINT/SIGTERM выполняется ограниченная таймаутом попытка запуска и ожидания готовности. При SIGKILL самого помощника, остановке Docker или потере хоста выполнить cleanup невозможно; при необходимости восстановите сервис через `sh scripts/dagger.sh deploy-app`.

Без Docker можно проверить защитные ветви (cleanup после ошибки, сохранение обеих причин при сбое cleanup, отказ от воздействия на нездоровый сервис, отклонение старой метрики):

```bash
python3 -m unittest discover -s scripts -p test_chaos_test.py -v
```

Проверено 4 ноября 2026 года: обе цели прошли stop/recovery и отдельную проверку автоматического перезапуска. Данные статусных панелей показали `1 → 0 → 1`, `RestartCount` увеличился после SIGKILL процесса; после тестов все восемь сервисов `healthy`. Четыре unit-теста защитных ветвей успешны; недопустимая цель отклоняется до воздействия на контейнеры.

Модуль находится в `.dagger/main.go`, конфигурация — в корневом `dagger.json`. Dagger запускает временный Docker CLI-контейнер с проброшенным сокетом. Тот запускает короткоживущий помощник на хостовом Docker с каталогом проекта, смонтированным по тому же абсолютному пути: так Compose корректно разрешает bind mounts на стороне демона. Secrets читаются помощником с хоста, а не копируются в слои образов или Dagger Directory. При создании файлов от root их владельцем назначается владелец каталога проекта.

Для Grafana используется отдельный временный curl-контейнер в сети мониторинга с проектом только для чтения. Данные авторизации передаются через временный curl-config и не выводятся. Используются API `/api/datasources/uid/...` и `/api/dashboards/db`, доступные в закреплённой Grafana 12.2.1; документация: [источники данных](https://grafana.com/docs/grafana/latest/developer-resources/api-reference/http-api/data_source/), [дашборды Grafana 12.2.1](https://github.com/grafana/grafana/blob/v12.2.1/docs/sources/developers/http_api/dashboard.md#create--update-dashboard), [Dagger sockets](https://docs.dagger.io/extending/arguments/#unix-sockets), [кэширование Dagger](https://docs.dagger.io/extending/function-caching/).

Откройте [дашборд здоровья контура](http://127.0.0.1:3000/d/safeboard-health). В нём 13 панелей: состояние MariaDB, HTTP-доступность Kuma, сбор метрик, ресурсы хоста и контейнеров, соединения БД и длительность HTTP-пробы. Дайте Prometheus накопить хотя бы минуту данных для графиков CPU. Изменения JSON применяются повторным `configure-grafana`; он перезапишет изменения управляемого дашборда, сделанные вручную в UI.

Проверено 4 ноября 2026 года: все три команды выполнились на существующем стенде; запросы всех 13 панелей вернули данные через API Grafana. Повторный `configure-grafana` сохранил ID источника и дашборда, дубликаты не появились. Запуск с полностью пустыми Docker-томами через Dagger отдельно не проверялся; генерация новых паролей проверена в пустом временном каталоге.

Ниже приведены прямой запуск через Compose и ручные проверки отдельных слоёв для диагностики.

## Требования и совместимость

Нужны Docker Engine или Docker Desktop, Docker Compose v2 с поддержкой `up --wait` и OpenSSL для создания паролей. Команды ниже выполняются из корня проекта в Bash, в том числе в WSL.

- Uptime Kuma закреплена на `2.5.9`. [Официальная документация](https://github.com/louislam/uptime-kuma/wiki/Environment-Variables) описывает поддержку внешней MariaDB с версии 2.0.0 и переменные `UPTIME_KUMA_DB_*`, включая `UPTIME_KUMA_DB_PASSWORD_FILE`.
- Выбрана MariaDB `11.4.13` из ветки 11.4; версия указана в [официальном списке релизов](https://mariadb.org/mariadb/all-releases/). В просмотренной документации Kuma нет матрицы сертифицированных пар версий; фактическая совместимость проверяется запуском и миграциями схемы.
- Тег Kuma `latest` не используем: согласно [документации тегов](https://github.com/louislam/uptime-kuma/wiki/Docker-Tags), он продолжает указывать на v1.

## Подготовка паролей

Создайте два разных пароля. Существующие файлы следующие команды не перезаписывают:

```bash
mkdir -p .secrets/app
chmod 700 .secrets/app
(umask 077; test -f .secrets/app/kuma_db_password || openssl rand -hex 32 > .secrets/app/kuma_db_password)
(umask 077; test -f .secrets/app/mariadb_root_password || openssl rand -hex 32 > .secrets/app/mariadb_root_password)
```

Пароли передаются через Compose secrets и не хранятся в YAML. Локальные secrets являются файлами на хосте, а не зашифрованным хранилищем. Kuma также записывает параметры подключения в `/app/data/db-config.json`, поэтому её том тоже содержит чувствительные данные. Каталог `.secrets` исключён из Git; для новой установки пароли создаются заново.

## Запуск и проверка

```bash
docker compose -f compose.app.yaml config --quiet
docker compose -f compose.app.yaml up -d --wait --wait-timeout 300
docker compose -f compose.app.yaml ps
curl -I http://127.0.0.1:3001/
```

Откройте [Uptime Kuma](http://127.0.0.1:3001/) и создайте учётную запись администратора при первом запуске. База выбирается автоматически через окружение. Если порт занят, задайте другой: `KUMA_PORT=3002 docker compose -f compose.app.yaml up -d --wait --wait-timeout 300`.

Kuma подключается к `mariadb:3306` по внутренней Compose-сети под пользователем `uptime_kuma`, имеющим права на одноимённую БД. Порт MariaDB не опубликован на хосте; веб-интерфейс опубликован только на loopback.

Проверка MariaDB — штатная `healthcheck.sh --connect --innodb_initialized`. Kuma ждёт `service_healthy` БД. Её HTTP-проверка принимает только 200 или 302; это проверка веб-сервера, а не полный тест SQL-запросов или работы мониторов. Параметры соответствуют [документации MariaDB](https://mariadb.com/docs/server/server-management/automated-mariadb-deployment-and-administration/docker-and-mariadb/using-healthcheck-sh) и [порядку запуска Compose](https://docs.docker.com/compose/how-tos/startup-order/).

Для диагностики:

```bash
docker compose -f compose.app.yaml logs --tail=100 mariadb uptime-kuma
```

## Хранение данных и перезапуск

Именованные тома `mariadb_data` и `kuma_data` хранят соответственно `/var/lib/mysql` и `/app/data`. Второй необходим и при внешней БД: там остаются конфигурация подключения, загрузки и другие файлы Kuma.

```bash
docker compose -f compose.app.yaml down
docker compose -f compose.app.yaml up -d --wait --wait-timeout 300
```

После повторного запуска созданная учётная запись и настройки должны сохраниться. `down` сохраняет тома; `down -v` удаляет данные. Тома обеспечивают сохранность при пересоздании контейнеров, но не заменяют резервное копирование.

Параметры создания пользователей и паролей MariaDB применяются только к пустому каталогу данных, как указано в [документации образа](https://mariadb.com/docs/server/server-management/automated-mariadb-deployment-and-administration/docker-and-mariadb/mariadb-server-docker-official-image-environment-variables). Изменение файла пароля после инициализации само по себе пароль в БД не меняет.

`unless-stopped` перезапускает завершившийся процесс, но статус `unhealthy` сам по себе перезапуска не вызывает. Зависимость `service_healthy` проверяется при запуске через Compose и не является постоянным контролем доступности БД.

## Результат локальной проверки

4 ноября 2026 года пара Kuma 2.5.9 и MariaDB 11.4.13 успешно запущена: оба сервиса `healthy`, схема Kuma содержит 28 таблиц, HTTP отвечает 302. SQL-подключение проверено из контейнера Kuma под пользователем приложения. Временные маркеры в БД и `/app/data` сохранились после `down` и повторного `up`; после проверки маркеры удалены. Администратор и пользовательские мониторы ещё не создавались.

## Слой мониторинга

Компоненты: Prometheus 3.7.3, Grafana 12.2.1, Node Exporter 1.10.2, cAdvisor 0.60.6, mysqld_exporter 0.17.2 и Blackbox Exporter 0.27.0. Версии закреплены в Compose.

- Node Exporter собирает CPU, RAM, load average, диски и файловые системы Linux-хоста. Сетевые коллекторы отключены, чтобы при работе в bridge-сети не выдавать сеть контейнера за сеть хоста.
- cAdvisor собирает ресурсы контейнеров, включая CPU, память, сеть и дисковый ввод-вывод, если соответствующие счётчики доступны в Docker/cgroups.
- mysqld_exporter подключается к MariaDB отдельным пользователем `monitoring` с лимитом в 3 соединения и рекомендованными upstream правами `PROCESS`, `REPLICATION CLIENT`, `SELECT`. Root-пароль ему не передаётся.
- Blackbox Exporter выполняет HTTP GET к Kuma, следует редиректам и проверяет итоговый статус 2xx. Метрики показывают доступность HTTP и длительность пробы, а не время выполнения внутренних мониторов Kuma.

Настройки основаны на документации [Node Exporter](https://github.com/prometheus/node_exporter#docker), [cAdvisor](https://github.com/google/cadvisor/blob/master/docs/running.md), [mysqld_exporter](https://github.com/prometheus/mysqld_exporter/tree/v0.17.2), [Blackbox Exporter](https://github.com/prometheus/blackbox_exporter/blob/v0.27.0/example.yml), [Prometheus](https://prometheus.io/docs/prometheus/latest/installation/) и [Grafana](https://grafana.com/docs/grafana/latest/setup-grafana/installation/docker/).

### Запуск мониторинга

Сначала запустите приложение по инструкции выше. Его сеть `safeboard-app_default` должна существовать: через неё только mysqld_exporter и Blackbox Exporter обращаются к приложению. Остальные сервисы находятся в отдельной сети мониторинга.

```bash
bash scripts/init-monitoring.sh
docker compose -f compose.monitoring.yaml config --quiet
docker compose -f compose.monitoring.yaml run --rm --no-deps --entrypoint promtool prometheus check config /etc/prometheus/prometheus.yml
docker compose -f compose.monitoring.yaml up -d --wait --wait-timeout 180
docker compose -f compose.monitoring.yaml ps
```

Скрипт создаёт пароли и пользователя мониторинга в уже работающей MariaDB; повторный запуск сохраняет пароли и повторно применяет права. Это работает и с уже заполненным томом БД, в отличие от скрипта в `docker-entrypoint-initdb.d`, который выполняется только на пустом томе. Не запускайте скрипт с `bash -x`: отладочная трассировка покажет секреты.

Prometheus доступен на [127.0.0.1:9090](http://127.0.0.1:9090/), Grafana — на [127.0.0.1:3000](http://127.0.0.1:3000/). Порты экспортёров на хост не публикуются. Логин Grafana — `admin`, пароль можно посмотреть локально:

```bash
cat .secrets/monitoring/grafana_admin_password
```

Secrets находятся в каталоге с правами 0700 и исключены из Git. Смонтированные файлы конфигурации экспортёра и пароля Grafana читаемы непривилегированными пользователями контейнеров. Пароль Grafana применяется при первом создании её БД; последующее изменение файла не меняет пароль существующей учётной записи.

### Ручная проверка метрик

1. Выполните `docker compose -f compose.monitoring.yaml ps`: все шесть сервисов должны быть `healthy`. Это проверяет HTTP-точки готовности процессов; состояние БД и Kuma оценивайте отдельными метриками ниже.
2. Откройте [Prometheus Targets](http://127.0.0.1:9090/targets). Через 30–60 секунд все шесть целей должны быть `UP`, а Last Error — пустым.
3. На странице [запросов Prometheus](http://127.0.0.1:9090/query) выполните выражения из таблицы. Для `rate` дайте накопиться хотя бы минуте данных.

| Что проверить | PromQL | Ожидаемый результат |
| --- | --- | --- |
| Сбор метрик | `up` | Шесть рядов со значением 1 |
| CPU хоста, % | `100 * (1 - avg(rate(node_cpu_seconds_total{job="node",mode="idle"}[1m])))` | Число от 0 до 100 |
| Использование RAM хоста, % | `100 * (1 - node_memory_MemAvailable_bytes{job="node"} / node_memory_MemTotal_bytes{job="node"})` | Число от 0 до 100 |
| Свободное место | `node_filesystem_avail_bytes{job="node",mountpoint="/"}` | Доступные байты корневой файловой системы |
| RAM контейнеров приложения | `container_memory_working_set_bytes{job="cadvisor",container_label_com_docker_compose_project="safeboard-app"}` | Отдельные ряды Kuma и MariaDB |
| CPU контейнеров, ядра | `rate(container_cpu_usage_seconds_total{job="cadvisor",container_label_com_docker_compose_project="safeboard-app"}[1m])` | Отдельные ряды Kuma и MariaDB |
| MariaDB | `mysql_up{job="mariadb"}` | 1 — экспортёр подключился к БД |
| Соединения MariaDB | `mysql_global_status_threads_connected{job="mariadb"}` | Число активных подключений |
| HTTP-доступность Kuma | `probe_success{job="kuma-http"}` | 1 — HTTP-проба успешна |
| Время HTTP-пробы Kuma, секунды | `probe_duration_seconds{job="kuma-http"}` | Неотрицательное значение |
| Итоговый HTTP-код Kuma | `probe_http_status_code{job="kuma-http"}` | 200 после перехода по редиректу |

`up{job="mariadb"}=1` означает доступность экспортёра, а `mysql_up=1` — успешное подключение к MariaDB. Аналогично, состояние Kuma показывает `probe_success`, а не `up` экспортёра. Отсутствие рядов нельзя считать нулевым потреблением или исправностью сервиса.

4. Откройте Grafana и войдите. После `configure-grafana` источник Prometheus и дашборд здоровья должны присутствовать. При прямом запуске только через Compose примените их отдельно: `sh scripts/dagger.sh configure-grafana`. Проверка API: `curl --fail http://127.0.0.1:3000/api/health` должна вернуть `database: ok`.

Диагностика:

```bash
docker compose -f compose.monitoring.yaml logs --tail=100
curl --fail http://127.0.0.1:9090/-/ready
curl --fail http://127.0.0.1:3000/api/health
```

### Постоянное хранение мониторинга

Prometheus хранит временные ряды в томе `prometheus_data` (`/prometheus`), срок хранения — 15 дней. Grafana хранит свою БД и настройки в `grafana_data` (`/var/lib/grafana`). Экспортёры не требуют томов с историей: историю записывает Prometheus, их конфигурации лежат в репозитории, секреты — в `.secrets`.

Для ручной проверки сохранности запомните время существующей точки на графике Prometheus, затем пересоздайте контейнеры:

```bash
docker compose -f compose.monitoring.yaml down
docker compose -f compose.monitoring.yaml up -d --wait --wait-timeout 180
```

Старая точка должна остаться в выбранном временном диапазоне; сохранённые настройки Grafana также должны остаться. Не добавляйте `-v`. Пока мониторинг подключён к сети приложения, для пересоздания обеих сетей останавливайте сначала мониторинг, затем приложение; запускайте в обратном порядке.

Проверено 4 ноября 2026 года: все шесть сервисов `healthy`, шесть целей Prometheus `UP`; метрики обоих контейнеров приложения поступают, `mysql_up=1`, HTTP-проба Kuma успешна. Метрики числа CPU, полного объёма RAM и размера корневой ФС сверены с хостом. После пересоздания контейнеров историческая точка Prometheus и изменённая тема Grafana сохранились; исходная тема восстановлена. Источник данных и дашборд не создавались.

### Доступ к ресурсам хоста

Стенд рассчитан на Linux Docker Engine со стандартным каталогом `/var/lib/docker` и rootful Docker. Node Exporter получает доступ к `/proc`, `/sys` и корню хоста только для чтения. cAdvisor работает привилегированно с доступом к Docker-сокету и cgroups; суффикс `:ro` у сокета не запрещает изменяющие запросы Docker API. Это доверенный инфраструктурный компонент.

В Docker Desktop метрики относятся к Linux-виртуальной машине Docker, а не всей macOS/Windows. Rootless Docker и нестандартный Docker Root Dir требуют адаптации монтирований и прав. Проверяйте наличие рядов обоих контейнеров приложения, а не только доступность cAdvisor.
