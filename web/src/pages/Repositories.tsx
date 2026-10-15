import { showClientGuide } from "../ClientGuide";
import React from "react";
import { Button, Message, Tag } from "@arco-design/web-react";
import { IconPlus } from "@arco-design/web-react/icon";
import { api, bytes } from "../api";
import { DataTable, ErrorBox, Row } from "../components";
import { Tools } from "../types";
export default function Repositories({
  repos,
  admin,
  form,
  perform,
  refresh,
  showText,
}: Omit<Tools, "rev" | "repos"> & {
  repos: { data: Row[] | null; error: string };
}) {
  return (
    <>
      <div className="section-toolbar">
        <span>
          仓库清单 <Tag>{repos.data?.length || 0}</Tag>
        </span>
        {admin && (
          <Button
            type="primary"
            icon={<IconPlus />}
            onClick={() =>
              form(
                "创建备份仓库",
                [
                  {
                    key: "name",
                    label: "仓库名称",
                    required: true,
                    help: "以字母开头，仅使用字母、数字、下划线或连字符。",
                  },
                  {
                    key: "kind",
                    label: "仓库类型",
                    type: "select",
                    value: "filesystem",
                    options: [
                      { label: "文件仓库 · NFS", value: "filesystem" },
                      { label: "块卷仓库 · iSCSI", value: "volume" },
                    ],
                  },
                  { key: "description", label: "用途说明" },
                  {
                    key: "quota",
                    label: "容量配额",
                    value: "100G",
                    help: "例如 100G、1T；块卷仓库同时作为卷大小。",
                  },
                  {
                    key: "compression",
                    label: "压缩算法",
                    type: "select",
                    value: "lz4",
                    options: ["lz4", "zstd", "off"].map((value) => ({
                      label: value,
                      value,
                    })),
                  },
                  {
                    key: "recordsize",
                    label: "文件记录大小",
                    type: "select",
                    value: "128K",
                    options: [
                      "16K",
                      "32K",
                      "64K",
                      "128K",
                      "256K",
                      "512K",
                      "1M",
                    ].map((value) => ({ label: value, value })),
                  },
                  {
                    key: "encrypted",
                    label: "启用原生加密",
                    type: "switch",
                    help: "密钥保存在设备私有目录；必须另行离线备份密钥。",
                  },
                ],
                async (v) => {
                  const j = await api("/repositories", "POST", {
                    name: v.name,
                    kind: v.kind,
                    description: v.description,
                    volume_size: v.quota,
                    encrypted: v.encrypted,
                    properties: {
                      quota: v.quota,
                      compression: v.compression,
                      recordsize: v.recordsize,
                    },
                  });
                  Message.success(`创建任务 #${j.id} 已提交`);
                  refresh();
                },
              )
            }
          >
            创建仓库
          </Button>
        )}
      </div>
      <ErrorBox error={repos.error} />
      <DataTable
        rows={repos.data}
        rowKey="repository.id"
        columns={[
          {
            title: "仓库 / 用途",
            render: (_: any, r: Row) => (
              <div>
                <strong>{r.repository.name}</strong>
                <small className="cell-sub">
                  {r.repository.description || r.repository.dataset}
                </small>
              </div>
            ),
          },
          {
            title: "类型",
            render: (_: any, r: Row) => (
              <Tag>{r.repository.kind === "volume" ? "块卷" : "文件仓库"}</Tag>
            ),
          },
          {
            title: "已用 / 可用",
            render: (_: any, r: Row) => (
              <span>
                {bytes(r.storage.used)} / {bytes(r.storage.available)}
              </span>
            ),
          },
          {
            title: "压缩 / 加密",
            render: (_: any, r: Row) => (
              <span>
                {r.storage.compression} ·{" "}
                {r.storage.encryption === "off"
                  ? "未加密"
                  : r.storage.key_status}
              </span>
            ),
          },
          {
            title: "状态",
            render: (_: any, r: Row) =>
              r.error ? (
                <Tag color="red">读取失败</Tag>
              ) : r.storage.key_status === "unavailable" ? (
                <Tag color="orange">密钥未加载</Tag>
              ) : (
                <Tag color="green">可用</Tag>
              ),
          },
          {
            title: "操作",
            width: 320,
            render: (_: any, r: Row) => (
              <div className="row-actions">
                <Button
                  type="text"
                  onClick={() =>
                    perform("snapshot.create", {
                      repository_id: r.repository.id,
                    })
                  }
                >
                  创建快照
                </Button>
                <Button
                  type="text"
                  onClick={() => showClientGuide(r.repository.id)}
                >
                  连接指南
                </Button>
                {admin && (
                  <>
                    <Button
                      type="text"
                      onClick={() =>
                        form(
                          "编辑仓库属性",
                          [
                            {
                              key: "quota",
                              label: "容量配额",
                              value:
                                r.storage.quota === "0"
                                  ? "none"
                                  : r.storage.quota,
                            },
                            {
                              key: "compression",
                              label: "压缩算法",
                              type: "select",
                              value: r.storage.compression,
                              options: ["lz4", "zstd", "off"].map((value) => ({
                                label: value,
                                value,
                              })),
                            },
                            {
                              key: "sync",
                              label: "同步写入",
                              type: "select",
                              value: "standard",
                              options: [
                                {
                                  label: "standard",
                                  value: "standard",
                                },
                                { label: "always", value: "always" },
                              ],
                            },
                          ],
                          async (v) => {
                            const j = await api(
                              "/repositories/" + r.repository.id,
                              "PATCH",
                              {
                                properties:
                                  r.repository.kind === "volume"
                                    ? {
                                        compression: v.compression,
                                        sync: v.sync,
                                      }
                                    : v,
                              },
                            );
                            Message.success(`属性任务 #${j.id} 已提交`);
                            refresh();
                          },
                        )
                      }
                    >
                      属性
                    </Button>
                    <Button
                      type="text"
                      status="danger"
                      onClick={() =>
                        form(
                          "删除仓库",
                          [
                            {
                              key: "confirm",
                              label: `输入仓库名称 ${r.repository.name}`,
                              required: true,
                            },
                          ],
                          (v) =>
                            perform("repository.delete", {
                              ...v,
                              repository_id: r.repository.id,
                            }),
                          "删除将永久移除仓库数据。存在快照、子数据集或协议导出时将拒绝执行。",
                        )
                      }
                    >
                      删除
                    </Button>
                    {r.storage.encryption !== "off" && (
                      <Button
                        type="text"
                        onClick={() =>
                          perform(
                            r.storage.key_status === "available"
                              ? "key.unload"
                              : "key.load",
                            { id: r.repository.id },
                          )
                        }
                      >
                        {r.storage.key_status === "available"
                          ? "卸载密钥"
                          : "加载密钥"}
                      </Button>
                    )}
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
