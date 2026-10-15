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
export default function SnapshotPage({
  snaps,
  repos,
  rev,
  admin,
  form,
  perform,
  browse,
  refresh,
}: Omit<Tools, "showText"> & {
  snaps: { data: Row[] | null; error: string };
  browse: (s: Row) => void;
}) {
  const policies = useData<Row[]>("/policies", rev);
  const [filter, setFilter] = useState<number | undefined>();
  function policy(r?: Row) {
    form(
      "快照与保留策略",
      [
        {
          key: "repository_id",
          label: "仓库",
          type: "select",
          options: repoOptions(repos),
          required: true,
          value: r?.repository_id,
        },
        {
          key: "cron",
          label: "Cron 调度（分 时 日 月 周）",
          value: r?.cron || "0 * * * *",
          required: true,
          help: "使用服务器 Asia/Shanghai 时区。例如每天 02:00：0 2 * * *",
        },
        {
          key: "keep_hourly",
          label: "小时级恢复点",
          type: "number",
          value: r?.keep_hourly ?? 24,
        },
        {
          key: "keep_daily",
          label: "天级恢复点",
          type: "number",
          value: r?.keep_daily ?? 30,
        },
        {
          key: "keep_weekly",
          label: "周级恢复点",
          type: "number",
          value: r?.keep_weekly ?? 12,
        },
        {
          key: "keep_monthly",
          label: "月级恢复点",
          type: "number",
          value: r?.keep_monthly ?? 12,
        },
        {
          key: "keep_yearly",
          label: "年级恢复点",
          type: "number",
          value: r?.keep_yearly ?? 3,
        },
        {
          key: "recursive",
          label: "包含子数据集",
          type: "switch",
          value: r?.recursive ?? false,
        },
        {
          key: "enabled",
          label: "启用自动快照",
          type: "switch",
          value: r?.enabled ?? true,
        },
      ],
      async (v) => {
        await api("/policies", "PUT", v);
        Message.success("策略已保存");
        refresh();
      },
    );
  }
  return (
    <>
      <div className="section-toolbar">
        <Select
          placeholder="全部仓库"
          allowClear
          value={filter}
          onChange={setFilter}
          options={repoOptions(repos)}
          style={{ width: 240 }}
        />
        <div className="row-actions">
          <Button
            onClick={() =>
              form(
                "对比快照空间变化",
                [
                  {
                    key: "from",
                    label: "起始快照",
                    type: "select",
                    options: (snaps.data || []).map((s) => ({
                      label: s.name,
                      value: s.name,
                    })),
                    required: true,
                  },
                  {
                    key: "to",
                    label: "目标快照",
                    type: "select",
                    options: (snaps.data || []).map((s) => ({
                      label: s.name,
                      value: s.name,
                    })),
                    required: true,
                  },
                ],
                async (v) => {
                  const source = snaps.data?.find((s) => s.name === v.from);
                  const target = snaps.data?.find((s) => s.name === v.to);
                  if (!source || source.repository_id !== target?.repository_id)
                    throw new Error("请选择同一仓库内的两个快照");
                  const data = await api(
                    "/snapshots/compare?" +
                      new URLSearchParams({
                        repository_id: String(source.repository_id),
                        from: v.from,
                        to: v.to,
                      }),
                  );
                  Modal.info({
                    title: "快照空间差异",
                    style: { width: 900 },
                    content: (
                      <pre className="code-block">{data.comparison}</pre>
                    ),
                  });
                },
              )
            }
          >
            对比快照
          </Button>
          {admin && <Button onClick={() => policy()}>配置快照策略</Button>}
          <Button
            type="primary"
            icon={<IconCamera />}
            onClick={() =>
              form(
                "创建快照",
                [
                  {
                    key: "repository_id",
                    label: "仓库",
                    type: "select",
                    options: repoOptions(repos),
                    required: true,
                  },
                  {
                    key: "name",
                    label: "自定义快照名称（可选）",
                    help: "留空将自动生成时间戳名称",
                  },
                  { key: "recursive", label: "递归快照", type: "switch" },
                ],
                (v) => perform("snapshot.create", v),
              )
            }
          >
            创建快照
          </Button>
        </div>
      </div>
      <ErrorBox error={snaps.error} />
      <DataTable
        rows={
          snaps.data?.filter((s) => !filter || s.repository_id === filter) || []
        }
        rowKey="name"
        columns={[
          { title: "快照名称", dataIndex: "name" },
          { title: "创建时间", dataIndex: "created_at", render: date },
          { title: "独占空间", dataIndex: "used", render: bytes },
          {
            title: "保护状态",
            render: (_: any, s: Row) => (
              <>
                {s.holds > 0 && <Tag color="green">已保护</Tag>}
                {s.clones !== "-" && s.clones && <Tag>有克隆</Tag>}
                {s.holds === 0 && s.clones === "-" && (
                  <span className="muted">常规保留</span>
                )}
              </>
            ),
          },
          {
            title: "操作",
            width: 300,
            render: (_: any, s: Row) => (
              <div className="row-actions">
                <Button type="text" onClick={() => browse(s)}>
                  浏览
                </Button>
                <Button
                  type="text"
                  onClick={() =>
                    perform("snapshot.hold", {
                      repository_id: s.repository_id,
                      snapshot: s.name,
                    })
                  }
                >
                  保护
                </Button>
                {admin && (
                  <>
                    <Button
                      type="text"
                      onClick={() =>
                        perform("snapshot.release", {
                          repository_id: s.repository_id,
                          snapshot: s.name,
                        })
                      }
                    >
                      解除保护
                    </Button>
                    <Button
                      type="text"
                      status="danger"
                      onClick={() =>
                        form(
                          "删除快照",
                          [
                            {
                              key: "confirm",
                              label: "输入完整快照名称",
                              required: true,
                            },
                          ],
                          (v) =>
                            perform("snapshot.delete", {
                              ...v,
                              repository_id: s.repository_id,
                              snapshot: s.name,
                            }),
                          "删除后无法通过此快照恢复。存在保护标记或克隆依赖时将拒绝删除。",
                        )
                      }
                    >
                      删除
                    </Button>
                  </>
                )}
              </div>
            ),
          },
        ]}
      />
      <section>
        <div className="section-title">
          <h2>自动快照策略</h2>
          <span className="muted">
            最新快照、保护点、克隆依赖和复制基线始终保留
          </span>
        </div>
        <DataTable
          rows={policies.data}
          columns={[
            {
              title: "仓库",
              dataIndex: "repository_id",
              render: (id: number) =>
                repos?.find((r) => r.repository.id === id)?.repository.name ||
                id,
            },
            {
              title: "调度",
              dataIndex: "cron",
              render: (v: string) => <code>{v}</code>,
            },
            {
              title: "分层保留",
              render: (_: any, p: Row) =>
                `时 ${p.keep_hourly} / 天 ${p.keep_daily} / 周 ${p.keep_weekly} / 月 ${p.keep_monthly} / 年 ${p.keep_yearly}`,
            },
            {
              title: "状态",
              dataIndex: "enabled",
              render: (v: boolean) => (
                <Tag color={v ? "green" : "gray"}>
                  {v ? "已启用" : "已暂停"}
                </Tag>
              ),
            },
            {
              title: "操作",
              render: (_: any, p: Row) => (
                <div className="row-actions">
                  {admin && (
                    <Button type="text" onClick={() => policy(p)}>
                      编辑
                    </Button>
                  )}
                  <Button
                    type="text"
                    onClick={() =>
                      api("/policies/" + p.repository_id + "/preview")
                        .then((d) =>
                          Modal.info({
                            title: "保留策略预览",
                            content: (
                              <div>
                                {d.snapshots.map((s: Row) => (
                                  <p key={s.name}>
                                    <Tag
                                      color={
                                        d.keep[s.name] ? "green" : "orange"
                                      }
                                    >
                                      {d.keep[s.name] ? "保留" : "将清理"}
                                    </Tag>
                                    {s.name}
                                  </p>
                                ))}
                              </div>
                            ),
                          }),
                        )
                        .catch((e) => Message.error(e.message))
                    }
                  >
                    预览
                  </Button>
                  {admin && (
                    <Button
                      type="text"
                      onClick={() =>
                        form(
                          "执行保留策略清理",
                          [
                            {
                              key: "confirm",
                              label: "输入 PRUNE 确认",
                              required: true,
                            },
                          ],
                          (v) => {
                            if (v.confirm !== "PRUNE")
                              throw new Error("确认文本不匹配");
                            return perform("retention.prune", {
                              id: p.repository_id,
                            });
                          },
                          "将按当前策略删除过期的平台快照，请先预览清理范围。",
                        )
                      }
                    >
                      立即清理
                    </Button>
                  )}
                </div>
              ),
            },
          ]}
        />
      </section>
    </>
  );
}
