#!/usr/bin/env python3
"""Build the committed Grafana dashboard with stable panel/data source UIDs."""
import json
from pathlib import Path

source = {"type": "prometheus", "uid": "safeboard-prometheus"}
panels = []


def panel(title, expression, kind="timeseries", unit="short", x=0, y=0, width=12, height=8, status=False):
    field = {"unit": unit, "color": {"mode": "palette-classic"}, "custom": {"spanNulls": False}}
    options = {"legend": {"displayMode": "list", "placement": "bottom"}}
    if status:
        field.update({"min": 0, "max": 1, "noValue": "Нет данных", "mappings": [
            {"type": "value", "options": {"0": {"text": "Всё плохо", "color": "red"}, "1": {"text": "Всё хорошо", "color": "green"}}},
            {"type": "special", "options": {"match": "null", "result": {"text": "Нет данных", "color": "orange"}}}],
            "thresholds": {"mode": "absolute", "steps": [{"color": "red", "value": None}, {"color": "green", "value": 1}]},
            "color": {"mode": "thresholds"}})
        options = {"colorMode": "background", "graphMode": "none", "textMode": "auto", "reduceOptions": {"calcs": ["last"], "fields": "", "values": False}}
    panels.append({"id": len(panels) + 1, "title": title, "type": kind, "datasource": source,
                   "gridPos": {"x": x, "y": y, "w": width, "h": height},
                   "fieldConfig": {"defaults": field, "overrides": []}, "options": options,
                   "targets": [{"refId": "A", "expr": expression, "legendFormat": "{{service}} {{instance}} {{container_label_com_docker_compose_service}}", "instant": status, "range": not status}]})


for x, title, metric in [(0, "Общий статус", "platform"), (6, "Uptime Kuma", "kuma"), (12, "MariaDB", "mariadb"), (18, "Наблюдаемость", "monitoring")]:
    panel(title, f"safeboard:{metric}_ok", "stat", x=x, y=0, width=6, height=4, status=True)
panel("История здоровья и Chaos Testing", 'safeboard:platform_ok', y=4, width=24, height=6)
panel("HTTP latency", 'probe_duration_seconds{job="blackbox"}', unit="s", x=0, y=10)
panel("Доступность HTTP и экспортёров", 'up', x=12, y=10)
filter_ = 'container_label_com_docker_compose_project=~"safeboard-(app|monitoring)",container_label_com_docker_compose_service!=""'
panel("CPU контейнеров", f'sum by (container_label_com_docker_compose_service) (rate(container_cpu_usage_seconds_total{{{filter_}}}[1m]))', unit="cores", y=18)
panel("RAM контейнеров", f'sum by (container_label_com_docker_compose_service) (container_memory_working_set_bytes{{{filter_}}})', unit="bytes", x=12, y=18)
panel("Сеть контейнеров RX", f'sum by (container_label_com_docker_compose_service) (rate(container_network_receive_bytes_total{{{filter_}}}[1m]))', unit="Bps", y=26)
panel("Дисковый ввод-вывод контейнеров", f'sum by (container_label_com_docker_compose_service) (rate(container_fs_reads_bytes_total{{{filter_}}}[1m]) + rate(container_fs_writes_bytes_total{{{filter_}}}[1m]))', unit="Bps", x=12, y=26)
panel("CPU хоста Docker", '1 - avg(rate(node_cpu_seconds_total{job="node",mode="idle"}[1m]))', unit="percentunit", y=34)
panel("RAM хоста Docker", 'node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes', unit="bytes", x=12, y=34)
panel("MariaDB соединения", 'mysql_global_status_threads_connected{job="mysql"}', y=42)
panel("MariaDB запросы в секунду", 'rate(mysql_global_status_questions{job="mysql"}[1m])', unit="reqps", x=12, y=42)
dashboard = {"id": None, "uid": "safeboard-health", "title": "Safeboard — здоровье контура", "tags": ["safeboard", "dagger"],
             "timezone": "browser", "schemaVersion": 41, "version": 0, "refresh": "5s", "editable": True,
             "time": {"from": "now-15m", "to": "now"}, "panels": panels,
             "annotations": {"list": [{"builtIn": 1, "datasource": {"type": "grafana", "uid": "-- Grafana --"}, "enable": True, "hide": False, "iconColor": "orange", "name": "Chaos events", "type": "dashboard"}]}}
output = Path(__file__).resolve().parent.parent / "config/grafana/dashboard.json"
output.parent.mkdir(parents=True, exist_ok=True)
output.write_text(json.dumps(dashboard, ensure_ascii=False, indent=2) + "\n")
