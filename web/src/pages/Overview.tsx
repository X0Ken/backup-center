import React, { useState } from "react";
import {
  Alert,
  Button,
  Message,
  Modal,
  Select,
  Tag,
  Progress,
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
  Status,
  Row,
  useData,
} from "../components";
import { Tools, repoOptions, kindLabels } from "../types";
export default function Overview({
  data,
  error,
  rev,
}: {
  data: Row | null;
  error: string;
  rev: number;
}) {
  const metrics = useData<Row[]>("/metrics?hours=24", rev);
  const pool = data?.pools?.[0];
  const used = pool?.Allocated || 0;
  const size = pool?.Size || 0;
  const percent = size ? (used / size) * 100 : 0;
  return (
    <>
      <ErrorBox error={error || data?.storage_error} />
      <div className="stat-grid">
        {[
          ["备份仓库", data?.repositories ?? "—", "独立保护空间", IconStorage],
          [
            "运行中的任务",
            data?.active_jobs ?? "—",
            "持久化执行队列",
            IconSync,
          ],
          [
            "待处理告警",
            data?.alerts ?? "—",
            "需要关注的事件",
            IconNotification,
          ],
          [
            "可用存储",
            pool ? bytes(pool.Free) : "—",
            "存储池剩余容量",
            IconDashboard,
          ],
        ].map(([label, value, sub, Icon]: any) => (
          <div className="stat-card" key={label}>
            <div>
              <span>{label}</span>
              <strong>{value}</strong>
              <small>{sub}</small>
            </div>
            <span className="stat-icon">
              <Icon />
            </span>
          </div>
        ))}
      </div>
      <div className="dashboard-grid">
        <section className="pool-card">
          <div className="section-title">
            <h2>存储池健康</h2>
            {pool && <Status value={pool.health} />}
          </div>
          <div className="pool-visual">
            <div className="storage-icon">
              <IconStorage />
            </div>
            <div>
              <h2>{pool?.name || "备份池未就绪"}</h2>
              <p className="muted">ZFS · 数据校验 · 压缩存储</p>
            </div>
          </div>
          <div className="pool-usage">
            <span>容量使用率</span>
            <strong>{percent.toFixed(1)}%</strong>
          </div>
          <Progress
            percent={Math.round(percent * 10) / 10}
            showText={false}
            color={percent > 80 ? "#e8a345" : "#19877b"}
          />
          <div className="pool-totals">
            <span>
              <i />
              已用 {bytes(used)}
            </span>
            <span>总计 {bytes(size)}</span>
          </div>
          <div className="pool-foot">
            <IconCheckCircle />
            <span>
              {pool?.health === "ONLINE"
                ? "存储池在线，数据保护服务可用"
                : "请检查池状态与告警信息"}
            </span>
          </div>
        </section>
        <section>
          <div className="section-title">
            <h2>容量变化</h2>
            <Tag>最近 24 小时</Tag>
          </div>
          {metrics.data?.length ? (
            <Chart data={metrics.data} />
          ) : (
            <div className="empty chart-empty">正在积累监控数据</div>
          )}
          <p className="chart-note">每分钟采集，保留最近 31 天历史记录</p>
        </section>
      </div>
      <section>
        <div className="section-title">
          <h2>最近任务</h2>
          <a href="#jobs">查看全部 →</a>
        </div>
        <DataTable
          rows={data?.recent_jobs || []}
          columns={[
            {
              title: "操作",
              dataIndex: "kind",
              render: (v: string) => kindLabels[v] || v,
            },
            ...jobColumns.slice(1),
          ]}
        />
      </section>
      <div className="guidance">
        <IconSafe />
        <div>
          <strong>备份的价值，在于数据能被恢复</strong>
          <p>
            为业务设置快照策略，并定期执行文件或数据库恢复演练。Mirror
            提供磁盘冗余，异地副本提供设备故障后的恢复能力。
          </p>
        </div>
        <a href="#restore">开始恢复验证 →</a>
      </div>
    </>
  );
}
