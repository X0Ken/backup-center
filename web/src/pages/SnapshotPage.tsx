import React, { useState } from "react";
import { Button, Modal, Select, Tag } from "@arco-design/web-react";
import { IconCamera } from "@arco-design/web-react/icon";
import { api, bytes, date } from "../api";
import { DataTable, ErrorBox, Row } from "../components";
import { Tools, repoOptions } from "../types";
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
  const [filter, setFilter] = useState<number | undefined>();
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
            width: 390,
            render: (_: any, s: Row) => (
              <div className="row-actions">
                <Button
                  type="text"
                  onClick={() =>
                    form(
                      "基于快照创建仓库",
                      [
                        {
                          key: "name",
                          label: "新仓库名称",
                          required: true,
                          help: "继承源仓库类型。创建后到共享访问页添加访问方式，再挂载新仓库查看内容。",
                        },
                      ],
                      (v) =>
                        perform("restore.clone", {
                          repository_id: s.repository_id,
                          snapshot: s.name,
                          name: v.name,
                        }),
                    )
                  }
                >
                  创建仓库
                </Button>
                {repos?.find((r) => r.repository.id === s.repository_id)
                  ?.repository.kind === "filesystem" && (
                  <Button type="text" onClick={() => browse(s)}>
                    浏览文件
                  </Button>
                )}
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
    </>
  );
}
