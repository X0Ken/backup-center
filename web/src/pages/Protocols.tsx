import { showClientGuide } from "../ClientGuide";
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
  function configure(row?: Row) {
    form(
      "配置协议导出",
      [
        {
          key: "repository_id",
          label: "仓库",
          type: "select",
          options: repoOptions(repos),
          value: row?.repository_id,
          required: true,
        },
        {
          key: "protocol",
          label: "访问协议",
          type: "select",
          value: row?.protocol || "nfs",
          options: [
            { label: "NFS · 文件仓库", value: "nfs" },
            { label: "iSCSI · 块卷仓库", value: "iscsi" },
          ],
        },
        {
          key: "clients",
          label: "NFS 客户端 IP / CIDR",
          value: row?.clients || "",
          help: "以空格分隔，例如 192.0.2.0/24。不允许通配符。",
        },
        {
          key: "read_only",
          label: "NFS 只读访问",
          type: "switch",
          value: row?.read_only || false,
        },
        {
          key: "iqn",
          label: "iSCSI Target IQN",
          value: row?.iqn || "iqn.2026-10.local.backup:repository",
        },
        {
          key: "initiators",
          label: "iSCSI Initiator IQN 白名单",
          type: "textarea",
          value: row?.initiators || "",
          help: "以空格分隔；仅白名单主机可连接。",
        },
        {
          key: "enabled",
          label: "启用导出",
          type: "switch",
          value: row?.enabled ?? true,
        },
      ],
      async (v) => {
        const j = await api("/exports", "PUT", v);
        Message.success(`配置任务 #${j.id} 已提交`);
        refresh();
      },
      "NFS 使用同步写入和 root_squash。iSCSI 应运行于隔离的存储网络，同一普通文件系统只能有一个写入端。",
    );
  }
  return (
    <>
      <div className="section-toolbar">
        <span>NFS 与 iSCSI 访问控制</span>
        {admin && (
          <Button
            type="primary"
            icon={<IconPlus />}
            onClick={() => configure()}
          >
            配置导出
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
            title: "协议",
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
                    配置
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
        <section>
          <h2>NFS 客户端</h2>
          <pre className="code-block">
            {connections.data?.nfs || "当前无已登记客户端"}
          </pre>
        </section>
        <section>
          <h2>iSCSI 会话</h2>
          <pre className="code-block">
            {connections.data?.iscsi ||
              connections.data?.iscsi_error ||
              "当前无会话"}
          </pre>
        </section>
      </div>
    </>
  );
}
