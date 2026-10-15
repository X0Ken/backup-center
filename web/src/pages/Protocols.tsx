import { showClientGuide } from "../ClientGuide";
import React from "react";
import { Button, Message, Tag } from "@arco-design/web-react";
import { IconPlus } from "@arco-design/web-react/icon";
import { api } from "../api";
import { DataTable, ErrorBox, Row, useData } from "../components";
import { Tools } from "../types";
export default function Protocols({
  rev,
  repos,
  admin,
  form,
  refresh,
  showText,
}: Omit<Tools, "perform">) {
  const data = useData<Row[]>("/exports", rev);
  const connections = useData("/connections", rev);
  const available =
    repos?.filter(
      (r) => !data.data?.some((e) => e.repository_id === r.repository.id),
    ) || [];
  function configure(row?: Row) {
    const kind = (v: Row) =>
      repos?.find((r) => r.repository.id === v.repository_id)?.repository.kind;
    const nfs = (v: Row) => kind(v) === "filesystem";
    const iscsi = (v: Row) => kind(v) === "volume";
    form(
      row ? "修改访问设置" : "添加访问方式",
      [
        {
          key: "repository_id",
          label: "仓库",
          type: "select",
          required: true,
          disabled: !!row,
          value: row?.repository_id,
          options: (row ? repos || [] : available).map((r) => ({
            value: r.repository.id,
            label: `${r.repository.name} · ${r.repository.kind === "volume" ? "iSCSI" : "NFS"}`,
          })),
          help: "访问方式由仓库类型决定：文件仓库使用 NFS，块卷仓库使用 iSCSI。",
        },
        {
          key: "clients",
          label: "允许访问的客户端 IP / CIDR",
          value: row?.clients || "",
          required: true,
          visible: nfs,
          help: "多个地址以空格分隔，例如 192.0.2.0/24。不允许通配符。",
        },
        {
          key: "read_only",
          label: "只读访问",
          type: "switch",
          value: row?.read_only || false,
          visible: nfs,
          help: "NFS 使用同步写入和 root_squash。查看快照内容时建议启用只读。",
        },
        {
          key: "iqn",
          label: "目标端 IQN（可选）",
          value: row?.iqn || "",
          visible: iscsi,
          help: "留空按仓库名称自动生成；访问启用后不能修改。",
        },
        {
          key: "initiators",
          label: "允许访问的客户端 IQN",
          type: "textarea",
          required: true,
          value: row?.initiators || "",
          visible: iscsi,
          help: "多个 IQN 以空格分隔。请使用隔离的存储网络；同一普通文件系统只允许一个写入端。",
        },
        {
          key: "enabled",
          label: "启用访问",
          type: "switch",
          value: row?.enabled ?? true,
        },
      ],
      async (v) => {
        const j = await api("/exports", "PUT", v);
        Message.success(`访问设置任务 #${j.id} 已提交`);
        refresh();
      },
    );
  }
  return (
    <>
      <div className="section-toolbar">
        <span>仓库访问方式与客户端权限</span>
        {admin && (
          <Button
            type="primary"
            icon={<IconPlus />}
            disabled={available.length === 0}
            onClick={() => configure()}
          >
            添加访问方式
          </Button>
        )}
      </div>
      <ErrorBox error={data.error} />
      <DataTable
        rows={data.data}
        columns={[
          {
            title: "仓库",
            dataIndex: "repository_id",
            render: (v: number) =>
              repos?.find((r) => r.repository.id === v)?.repository.name || v,
          },
          {
            title: "访问方式",
            dataIndex: "protocol",
            render: (v: string) => (
              <Tag color="arcoblue">{v.toUpperCase()}</Tag>
            ),
          },
          {
            title: "允许访问",
            render: (_: any, r: Row) => (
              <code>{r.protocol === "nfs" ? r.clients : r.initiators}</code>
            ),
          },
          {
            title: "状态",
            dataIndex: "enabled",
            render: (v: boolean) => (
              <Tag color={v ? "green" : "gray"}>{v ? "已启用" : "已关闭"}</Tag>
            ),
          },
          {
            title: "操作",
            render: (_: any, r: Row) => (
              <div className="row-actions">
                {admin && (
                  <Button type="text" onClick={() => configure(r)}>
                    修改访问设置
                  </Button>
                )}
                <Button
                  type="text"
                  onClick={() => showClientGuide(r.repository_id)}
                >
                  接入指南
                </Button>
              </div>
            ),
          },
        ]}
      />
      <div className="two-cols">
        {data.data?.some((e) => e.protocol === "nfs") && (
          <section>
            <h2>NFS 客户端</h2>
            <pre className="code-block">
              {connections.data?.nfs || "当前无已登记客户端"}
            </pre>
          </section>
        )}
        {data.data?.some((e) => e.protocol === "iscsi") && (
          <section>
            <h2>iSCSI 会话</h2>
            <pre className="code-block">
              {connections.data?.iscsi ||
                connections.data?.iscsi_error ||
                "当前无会话"}
            </pre>
          </section>
        )}
      </div>
    </>
  );
}
