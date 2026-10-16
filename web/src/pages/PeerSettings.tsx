import React from "react";
import { Button, Message, Modal, Tag } from "@arco-design/web-react";
import { api, date } from "../api";
import { DataTable, ErrorBox, Row, useData } from "../components";
import { Tools } from "../types";
export default function PeerSettings({
  rev,
  form,
  refresh,
  showText,
}: Pick<Tools, "rev" | "form" | "refresh" | "showText">) {
  const peers = useData<Row[]>("/peers", rev);
  return (
    <section>
      <h2>异地复制平台互信</h2>
      <p className="muted">
        两端须部署相同版本。本机备份目标只需配置一次，复制任务自动使用该平台。在目标平台生成配对码，再在本机填写目标地址并导入。
      </p>
      <div className="settings-actions">
        <Button
          onClick={async () => {
            try {
              const r = await api("/peers/invitation", "POST", {});
              showText({
                title: `接收配对码（一次有效，至 ${date(r.expires_at)}）`,
                content: r.code,
              });
            } catch (e: any) {
              Message.error(e.message);
            }
          }}
        >
          生成接收配对码
        </Button>
        <Button
          type="primary"
          onClick={() =>
            form(
              "连接目标平台",
              [
                {
                  key: "name",
                  label: "目标名称",
                  value: "异地备份平台",
                  required: true,
                },
                {
                  key: "url",
                  label: "目标平台 HTTPS 地址",
                  required: true,
                  help: "例如 https://192.0.2.11；目标平台必须部署相同版本。",
                },
                {
                  key: "code",
                  label: "目标平台生成的配对码",
                  type: "password",
                  required: true,
                },
              ],
              async (v) => {
                await api("/peers", "POST", v);
                Message.success("目标平台已连接");
                refresh();
              },
            )
          }
        >
          {peers.data?.some((p) => p.direction === "outbound")
            ? "更新目标连接"
            : "连接目标平台"}
        </Button>
      </div>
      <ErrorBox error={peers.error} />
      <DataTable
        rows={peers.data}
        columns={[
          {
            title: "平台",
            render: (_: any, p: Row) => (
              <span>
                {p.name || p.instance_id}
                <small className="cell-sub">{p.url || "已授权的发送端"}</small>
              </span>
            ),
          },
          {
            title: "用途",
            render: (_: any, p: Row) => (
              <Tag>
                {p.direction === "outbound" ? "本机备份目标" : "允许接收其备份"}
              </Tag>
            ),
          },
          { title: "建立时间", dataIndex: "created_at", render: date },
          {
            title: "操作",
            render: (_: any, p: Row) => (
              <Button
                type="text"
                status="danger"
                onClick={() =>
                  Modal.confirm({
                    title: "移除平台信任",
                    content:
                      "移除后将停止允许该连接，已有远端仓库和备份数据保留。发送端须先删除关联任务。",
                    onOk: async () => {
                      await api("/peers/" + p.id, "DELETE");
                      refresh();
                    },
                  })
                }
              >
                移除信任
              </Button>
            ),
          },
        ]}
      />
    </section>
  );
}
