import { useRepositoryFilter } from "../navigation";
import React from "react";
import {
  Alert,
  Button,
  Message,
  Modal,
  Select,
  Tag,
} from "@arco-design/web-react";
import { api, date } from "../api";
import { DataTable, ErrorBox, Row, useData } from "../components";
import { Tools, repoOptions } from "../types";
export function editPolicy(
  form: Tools["form"],
  repos: Row[] | null,
  refresh: () => void,
  r?: Row,
  repositoryID?: number,
) {
  form(
    r ? "编辑快照策略" : "添加快照策略",
    [
      {
        key: "repository_id",
        label: "仓库",
        type: "select",
        options: repoOptions(repos),
        disabled: !!r || !!repositoryID,
        required: true,
        value: r?.repository_id ?? repositoryID,
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
        label: "年度恢复点",
        help: (v) =>
          `自动快照频率：${v.keep_hourly > 0 ? "每小时整点" : v.keep_daily > 0 ? "每天零点" : v.keep_weekly > 0 ? "每周一零点" : v.keep_monthly > 0 ? "每月 1 日零点" : v.keep_yearly > 0 ? "每年 1 月 1 日零点" : "请至少启用一个保留层级"}（服务器时区）`,
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

export default function Policies({
  repos,
  rev,
  admin,
  form,
  perform,
  refresh,
}: Omit<Tools, "showText">) {
  const [repositoryFilter, setRepositoryFilter] = useRepositoryFilter();
  const policies = useData<Row[]>("/policies", rev);
  function policy(r?: Row) {
    editPolicy(
      form,
      repos?.filter((x) =>
        r
          ? x.repository.id === r.repository_id
          : !policies.data?.some((p) => p.repository_id === x.repository.id),
      ) || [],
      refresh,
      r,
    );
  }
  return (
    <>
      <div className="section-toolbar">
        <Select
          aria-label="按仓库筛选策略"
          placeholder="全部仓库"
          allowClear
          value={repositoryFilter}
          onChange={setRepositoryFilter}
          options={repoOptions(repos)}
          style={{ width: 240 }}
        />
        {admin && (
          <Button
            type="primary"
            disabled={
              !repos?.some(
                (r) =>
                  !policies.data?.some(
                    (p) => p.repository_id === r.repository.id,
                  ),
              )
            }
            onClick={() => policy()}
          >
            添加快照策略
          </Button>
        )}
      </div>
      <Alert content="系统按最细的非零保留层级自动创建快照，并按服务器时区分层保留。启用和恢复运行时补建当前周期缺失的恢复点；历史恢复点随时间积累，设为 0 表示关闭该层。" />
      <ErrorBox error={policies.error} />
      <section>
        <div className="section-title">
          <h2>自动快照策略</h2>
          <span className="muted">
            最新快照、保护点、克隆依赖和复制基线始终保留
          </span>
        </div>
        <DataTable
          rows={
            policies.data?.filter(
              (p) => !repositoryFilter || p.repository_id === repositoryFilter,
            ) || null
          }
          tableName="快照策略"
          filterKey={String(repositoryFilter)}
          searchText={(p) =>
            `${repos?.find((r) => r.repository.id === p.repository_id)?.repository.name || ""} ${p.enabled ? "已启用" : "已暂停"}`
          }
          columns={[
            {
              title: "仓库",
              dataIndex: "repository_id",
              render: (id: number) =>
                repos?.find((r) => r.repository.id === id)?.repository.name ||
                id,
            },
            {
              title: "自动快照频率",
              dataIndex: "schedule_label",
              render: (v: string, p: Row) => (
                <span>
                  {v}
                  <small className="cell-sub">
                    {p.timezone}
                    {p.next_run_at ? ` · 下次 ${date(p.next_run_at)}` : ""}
                  </small>
                </span>
              ),
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
