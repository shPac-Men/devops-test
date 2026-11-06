# Локальный контур Uptime Kuma

Два независимых Compose-слоя: Kuma + MariaDB и мониторинг (Prometheus, Grafana, Node Exporter, cAdvisor, mysqld_exporter, Blackbox Exporter). Запуском управляет Go-модуль Dagger. Реализованы `deploy-all` и chaos testing; Galera пока нет.

Prometheus закреплён на версии **3.13.5 LTS**, Grafana — **13.3.0**.

## Подготовка

Нужны Linux с rootful Docker Engine, стандартный каталог `/var/lib/docker`, сокет `/var/run/docker.sock` и Dagger CLI **v0.21.11** в PATH. Для загрузки образов требуется доступ в интернет. Порты 3000, 3001 и 9090 должны быть свободны.

Распакуйте архив в отдельный каталог, перейдите в него и проверьте окружение:

```bash
docker info
dagger version
```

Docker должен быть доступен текущему пользователю без `sudo`. Локальные Go, Python и Compose для запуска через Dagger не нужны: инструменты работают в контейнерах. В Docker Desktop метрики относятся к Linux-VM Docker; rootless Docker и нестандартные пути требуют адаптации.

## Запуск

Все команды выполняются из каталога с `dagger.json`:

```bash
dagger call deploy-all
```

Или запустите этапы отдельно, в этом порядке:

```bash
dagger call deploy-app
dagger call deploy-monitoring
dagger call configure-grafana
```

Пароли создаются автоматически в `.secrets`; существующие пароли и тома сохраняются. `configure-grafana` создаёт или обновляет источник Prometheus и дашборд без дубликатов. При ошибке `deploy-all` останавливается и указывает этап и причину; после исправления его можно повторить.

Четвёртый этап `deploy-all`, `validate-platform`, в течение общего таймаута 120 секунд ждёт шесть ожидаемых целей Prometheus со статусом `UP`, свежие метрики (не старше 30 секунд), `mysql_up=1` и успешную HTTP-пробу Kuma. Через Grafana выполняются запросы всех 13 сохранённых панелей: проверяются статусы, конечные числовые значения и наличие CPU/RAM хоста и обоих контейнеров приложения. Для CPU ожидается накопление истории. При ошибке этап возвращает ненулевой код с конкретной причиной. Проверка подтверждает данные панелей; внешний вид проверяется в браузере.

Дополнительные аргументы не нужны: путь проекта определяется из локального workspace Dagger, а файл `.env` из проекта задаёт `dockerSocket=/var/run/docker.sock`. Этот файл содержит только публичную настройку и должен входить в архив; пароли хранятся отдельно в `.secrets`.

При необходимости путь и сокет можно переопределить:

```bash
dagger call --project-dir="$PWD" --docker-socket=/var/run/docker.sock deploy-all
```

Также доступна обёртка `sh scripts/dagger.sh deploy-all`: она использует `dagger` из PATH или `.tools/dagger` и те же настройки по умолчанию.

## Доступ и проверка

| Сервис | Адрес | Что проверить |
| --- | --- | --- |
| Uptime Kuma | http://127.0.0.1:3001/ | При первом входе создать администратора |
| Prometheus | http://127.0.0.1:9090/targets | Через 30–60 секунд все 6 целей `UP` |
| Grafana | http://127.0.0.1:3000/d/safeboard-health | На 13 панелях есть данные; CPU требует около минуты истории |

Grafana: логин `admin`, начальный пароль:

```bash
cat .secrets/monitoring/grafana_admin_password
```

На новой установке создаётся новый пароль. Изменение файла не меняет пароль уже созданного пользователя Grafana. MariaDB на хост не публикуется; веб-интерфейсы доступны только через loopback. На удалённом стенде используйте браузер самого стенда или SSH-туннель.

При наличии Docker Compose на хосте проверьте все 8 сервисов — ожидается `healthy`:

```bash
docker compose -f compose.app.yaml ps
docker compose -f compose.monitoring.yaml ps
```

В Prometheus проверьте `mysql_up{job="mariadb"}` и `probe_success{job="kuma-http"}`: оба значения должны быть 1. `probe_duration_seconds{job="kuma-http"}` показывает длительность HTTP-пробы в секундах. Метрика `up` подтверждает сбор метрик, а не исправность самой БД или Kuma.

## Chaos testing

После `deploy-all` запускайте цели по одной, наблюдая за соответствующей панелью Grafana:

```bash
dagger call chaos-test --target=mariadb
dagger call chaos-test --target=uptime-kuma
```

Тест проверяет свежие данные панели `1 → 0 → 1`, затем отдельно автоматический перезапуск после завершения основного процесса. После ручного stop нужен явный start — [restart-policy Docker](https://docs.docker.com/engine/containers/start-containers-automatically/#restart-policy-details) подавляется ручной остановкой. Данные панели проверяются через API, без скриншотов.

`--timeout=120` задаёт таймаут каждой фазы (30–600 секунд). При ошибке выполняется попытка восстановления сервиса; после аварии самого помощника или Docker при необходимости выполните `deploy-app`. Тест использует хостовые PID/network namespaces и Docker-сокет. Одновременно два теста не запускайте.

Проверки защитных ветвей без Docker, если установлен Python 3.11+:

```bash
python3 -m unittest discover -s scripts -p test_chaos_test.py -v
```

## Данные и диагностика

Именованные тома сохраняют MariaDB (`/var/lib/mysql`), файлы Kuma (`/app/data`), Prometheus (`/prometheus`, история 15 дней) и Grafana (`/var/lib/grafana`). `.secrets` и тома содержат чувствительные данные; secrets не включайте в архив. Изменение файла пароля MariaDB после инициализации не меняет пароль в БД.

Для проверки сохранности пересоздайте слои **без `-v`** и убедитесь, что учётная запись и настройки Kuma, история метрик и Grafana сохранились:

```bash
docker compose -f compose.monitoring.yaml down
docker compose -f compose.app.yaml down
dagger call deploy-all
```

Логи для диагностики:

```bash
docker compose -f compose.app.yaml logs --tail=100
docker compose -f compose.monitoring.yaml logs --tail=100
```

Если Dagger зависает до запуска этапов, проверьте `docker info`, доступ к реестрам образов и логи контейнера `dagger-engine` (имя найдите через `docker ps -a --filter name=dagger-engine`). Полную независимую проверку выполняйте из архива в чистой Linux-VM: текущий стенд уже содержит образы и тома.
