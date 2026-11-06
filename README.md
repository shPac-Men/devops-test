# Локальный контур Uptime Kuma

Два Compose-слоя под управлением Dagger:

- Приложение: Uptime Kuma → HAProxy → 3 узла MariaDB Galera с отдельными томами.
- Мониторинг: Prometheus, Grafana, Node Exporter, cAdvisor, 3 mysqld_exporter и Blackbox Exporter.

## Подготовка

Нужны Linux с rootful Docker Engine, Docker Compose v2 и Dagger CLI **v0.21.11**. Docker должен быть доступен без `sudo`; ожидаются `/var/run/docker.sock` и `/var/lib/docker`. Для загрузки образов нужен интернет, порты 3000, 3001 и 9090 должны быть свободны. В Docker Desktop метрики хоста относятся к Linux-VM; rootless Docker требует адаптации. Рекомендуется ≥ 6 ГБ RAM, ≥ 10 ГБ свободного диска и 2+ vCPU (13 контейнеров + тома Galera/Prometheus).

**1. Установите Dagger CLI v0.21.11** (если ещё не установлен):
```bash
# https://docs.dagger.io/install
curl -fsSL https://dl.dagger.io/dagger/install.sh | sh
# либо используйте бинарник из .tools/dagger — обёртка scripts/dagger.sh его подхватит
```
**2. Перейдите в каталог проекта**, выдайте права на скрипты (после распаковки ZIP бит `+x` может потеряться) и проверьте инструменты:
```bash
cd tets-devops
chmod +x scripts/*.sh
docker info
docker compose version
dagger version
```
**3. Подготовьте `.env`.** В поставке лежит `.env.example` (без секретов); `.env` создаётся из него:
```bash
cp .env.example .env
# при необходимости поправьте DOCKER_SOCKET в .env
```
`.env` задаёт публичный путь Docker-сокета. Пароли автоматически создаются в `.secrets/` при первом `dagger call deploy-all` (или отдельно `deploy-monitoring`); до первого запуска файлов в `.secrets/` нет. Существующие пароли и данные при повторных запусках сохраняются. Каталог `.secrets/` в поставку не входит и в архив включаться не должен.

## Запуск

```bash
sh scripts/dagger.sh deploy-all
```

Обёртка использует Dagger из PATH или `.tools/dagger`. Эквивалентная команда — `dagger call deploy-all`. Отдельные этапы выполняются в таком порядке:

```bash
dagger call deploy-app
dagger call deploy-monitoring
dagger call configure-grafana
```

`deploy-all` дополнительно проверяет 9 целей Prometheus, свежесть метрик, состояние трёх узлов Galera и данные 17 панелей Grafana. При ошибке выводится причина, выполнение останавливается. CPU-графикам нужно около минуты истории.

## Доступ и проверка

| Сервис | Адрес | Ожидаемый результат |
| --- | --- | --- |
| Uptime Kuma | [127.0.0.1:3001](http://127.0.0.1:3001/) | При первом входе создать администратора |
| Prometheus | [127.0.0.1:9090/targets](http://127.0.0.1:9090/targets) | Все 9 целей `UP` |
| Grafana | [Дашборд здоровья](http://127.0.0.1:3000/d/safeboard-health) | Есть данные, статусы исправны |

Grafana: логин `admin`, начальный пароль:

```bash
cat .secrets/monitoring/grafana_admin_password
```

Веб-интерфейсы доступны через loopback, SQL-порты наружу не опубликованы. Для удалённого доступа используйте SSH-туннель. Изменение файлов паролей не меняет пароли уже созданных пользователей БД и Grafana.

На каждом узле ожидаются `mysql_up=1`, размер Galera `3` и состояние Synced `4`. Общая панель Galera проверяет все три узла; панель HAProxy показывает наличие готового backend, а не результат SQL-запроса. Метрика `up` означает успешный сбор метрик.

## Остановка и повторный запуск

```bash
# Остановить Kuma, HAProxy и поочерёдно узлы Galera
sh scripts/dagger.sh stop-app
# Остановить мониторинг
docker compose -f compose.monitoring.yaml stop
```

Контейнеры и данные сохраняются. Для запуска снова выполните:

```bash
sh scripts/dagger.sh deploy-all
```

Для Galera используйте `stop-app`: последний остановленный узел сохраняет `safe_to_bootstrap=1`. Одновременная остановка через `compose down` может потребовать ручного восстановления. `down -v` удаляет тома с данными.

## Проверки отказов

Запускайте после `deploy-all`, строго по одной:

```bash
# Запись через HAProxy при остановке первого узла и проверка репликации
sh scripts/dagger.sh test-galera
# Переход панели 1 → 0 → 1 и автоматический рестарт после сбоя процесса
sh scripts/dagger.sh chaos-test --target=mariadb
sh scripts/dagger.sh chaos-test --target=uptime-kuma
```

Цель `mariadb` — первый узел. При его отказе общая панель Galera становится красной, а HAProxy может продолжать обслуживать БД. После ручного `stop` тест явно запускает контейнер: restart-policy действует при сбое процесса. При ошибке тест пытается восстановить сервис. Не совмещайте проверки отказов с развёртыванием.

## Galera и аварийное восстановление

HAProxy предпочитает первый узел и допускает только Primary/Synced/Ready/Connected. Переключение может вызвать краткие ошибки подключений. SST использует `rsync`, временно блокирующий донора; межузловой трафик не шифруется. Один Docker-хост и один HAProxy остаются едиными точками отказа.

При миграции работающей одиночной БД первый узел сохраняет прежний том, а SQL-копия приложения помещается в `.secrets/backups/`.

Если после общей аварии нет безопасного bootstrap-кандидата, `deploy-app` откажется создавать новый Primary:

1. Остановите все узлы и приложение; убедитесь, что работающего Primary нигде нет. Сделайте копии трёх томов.
2. Сверьте `grastate.dat`; при `seqno=-1` получите восстановленную позицию через `mariadbd --wsrep-recover` на остановленном томе.
3. Выберите узел с максимальным seqno в общей UUID-истории. Разрешите `safe_to_bootstrap: 1` только ему; на остальных оставьте 0. Разные UUID требуют отдельного разбора.
4. Выполните `deploy-all` и проверьте данные. Не выбирайте пустой или отставший том для bootstrap.

Подробности — в [документации MariaDB по восстановлению](https://mariadb.com/docs/galera-cluster/high-availability/recovering-a-primary-component).

## Диагностика

```bash
docker compose -f compose.app.yaml ps
docker compose -f compose.monitoring.yaml ps
docker compose -f compose.app.yaml logs --tail=100
docker compose -f compose.monitoring.yaml logs --tail=100
```

Ожидаются 13 сервисов со статусом `healthy`. Если Dagger не начинает выполнение, проверьте `docker info`, доступ к реестрам и логи контейнера `dagger-engine`. Журнал использования AI — в [AI_REVIEW.md](AI_REVIEW.md).
