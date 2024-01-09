# IotaWatt Upload Proxy

The [IotaWatt](https://iotawatt.com/) device is an open source / open hardware Electricity Monitor. It can optionally export the recorded data in a number of formats. I use InfluxDB v1 line protocol. That said, I prefer to store the data using [VictoriaMetrics](https://victoriametrics.com/), because it's smaller, less resource intensive, and I can use the more familiar MetricsQL in Grafana. This works well, because VictoriaMetrics [natively supports the InfluxDB line write protocol](https://docs.victoriametrics.com/guides/migrate-from-influx.html).

However, there is a small issue: when starting up, the IotaWatt device will query the remote storage endpoint to see what it has already uploaded. Then it can just start re-uploading from that point, instead of having to re-upload everything. Unfortunately, while VictoriaMetrics supports InfluxDB *write* protocol, it doesn't support Influx query language.

Thus this project aims to solve that small issue. This server acts a reverse proxy infront of VictoriaMetrics. Query requests are converted to MetricsQL queries, and the responses are formatted in Influx query response format. Any other requests are proxied directly to VictoriaMetrics. This allows IotaWatt to pick up where it left off writing.
