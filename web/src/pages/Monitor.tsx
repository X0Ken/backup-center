import React, { useState } from "react";
import {
  Alert,
  Button,
  Message,
  Modal,
  Select,
  Tag,
} from "@arco-design/web-react";
import {
  IconDashboard,
  IconStorage,
  IconSync,
  IconNotification,
  IconCheckCircle,
  IconSafe,
  IconCamera,
  IconPlus,
  IconHistory,
} from "@arco-design/web-react/icon";
import { api, bytes, date, useAuth } from "../api";
import {
  Chart,
  DataTable,
  ErrorBox,
  jobColumns,
  Row,
  useData,
} from "../components";
import { Tools, repoOptions, kindLabels } from "../types";
export default function Monitor({
  rev,
  admin,
  perform,
  refresh,
}: Pick<Tools, "rev" | "admin" | "perform" | "refresh">) {
  const d = useData("/monitor", rev);
  const [hours, setHours] = useState(24);
  const metrics = useData<Row[]>("/metrics?hours=" + hours, rev);
  const last = metrics.data?.at(-1);
  return (
    <>
      <div className="section-toolbar">
        <Select
          value={hours}
          onChange={setHours}
          options={[
            { label: "最近 1 小时", value: 1 },
            { label: "最近 24 小时", value: 24 },
            { label: "最近 7 天", value: 168 },
            { label: "最近 31 天", value: 744 },
          ]}
          style={{ width: 180 }}
        />
        {admin && (
          <Button onClick={() => perform("pool.scrub", {})}>
            启动存储池校验
          </Button>
        )}
      </div>
      <div className="stat-grid">
        {[
          ["读 IOPS（启动以来均值）", last?.read_ops ?? "—"],
          ["写 IOPS（启动以来均值）", last?.write_ops ?? "—"],
          ["读取吞吐（均值）", last ? bytes(last.read_bytes) + "/s" : "—"],
          ["ARC 缓存命中率", last ? last.arc_hit.toFixed(1) + "%" : "—"],
        ].map(([k, v]) => (
          <div className="stat-card" key={k}>
            <div>
              <span>{k}</span>
              <strong>{v}</strong>
            </div>
          </div>
        ))}
      </div>
      <section>
        <div className="section-title">
          <h2>存储容量趋势</h2>
          <span className="muted">每分钟采集</span>
        </div>
        <Chart data={metrics.data} />
      </section>
      <section>
        <h2>告警事件</h2>
        <DataTable
          rows={null}
          endpoint="/alerts"
          revision={rev}
          tableName="告警"
          columns={[
            {
              title: "级别",
              dataIndex: "severity",
              render: (v: string) => (
                <Tag color={v === "critical" ? "red" : "orange"}>
                  {v === "critical" ? "严重" : "警告"}
                </Tag>
              ),
            },
            { title: "事件", dataIndex: "message" },
            { title: "最近发生", dataIndex: "updated_at", render: date },
            {
              title: "状态",
              render: (_: any, r: Row) => (
                <Tag color={r.active ? "orange" : "green"}>
                  {r.active ? (r.acknowledged ? "已确认" : "待处理") : "已恢复"}
                </Tag>
              ),
            },
            {
              title: "操作",
              render: (_: any, r: Row) =>
                !r.acknowledged && r.active ? (
                  <Button
                    type="text"
                    onClick={async () => {
                      await api("/alerts/" + r.id + "/ack", "POST", {});
                      refresh();
                    }}
                  >
                    确认告警
                  </Button>
                ) : null,
            },
          ]}
        />
      </section>
      <div className="two-cols">
        <section>
          <h2>池拓扑与校验状态</h2>
          <ErrorBox error={d.error || d.data?.error} />
          <pre className="code-block">{d.data?.pool_status || "读取中…"}</pre>
        </section>
        <section>
          <h2>磁盘健康</h2>
          <DataTable
            rows={d.data?.disks || []}
            rowKey="device"
            columns={[
              { title: "设备", dataIndex: "device" },
              {
                title: "型号",
                render: (_: any, r: Row) =>
                  r.smart?.model_name || r.smart?.model_family || "—",
              },
              {
                title: "温度",
                render: (_: any, r: Row) =>
                  r.smart?.temperature?.current !== undefined
                    ? `${r.smart.temperature.current} °C`
                    : "—",
              },
              {
                title: "SMART",
                render: (_: any, r: Row) =>
                  r.error ? (
                    <Tag color="orange">读取失败</Tag>
                  ) : (
                    <Tag
                      color={r.smart?.smart_status?.passed ? "green" : "red"}
                    >
                      {r.smart?.smart_status?.passed ? "通过" : "需检查"}
                    </Tag>
                  ),
              },
            ]}
          />
        </section>
      </div>
    </>
  );
}
