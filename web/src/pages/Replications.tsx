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
export default function Replications({
  rev,
  repos,
  admin,
  form,
  perform,
  refresh,
  showText,
}: Tools) {
  const reps = useData<Row[]>("/replications", rev);
  return (
    <>
      <div className="guidance">
        <IconSync />
        <div>
          <strong>为设备级故障保留独立副本</strong>
          <p>
            使用 SSH 加密传输、严格主机验证、自动增量和中断续传。接收端快照 GUID
            校验成功后，才推进复制基线。
          </p>
        </div>
      </div>
      <div className="section-toolbar">
        <span>异地复制任务</span>
        {admin && (
          <div className="row-actions">
            <Button
              onClick={() =>
                api("/ssh")
                  .then((d) =>
                    showText({
                      title: "复制 SSH 公钥与信任主机",
                      content:
                        "请将以下公钥添加到目标端专用备份账号的 authorized_keys，使用 zfs allow 授予目标数据集所需权限。\n\n" +
                        d.public_key +
                        "\n已信任主机：\n" +
                        (d.known_hosts || "无") +
                        "\n在目标服务器运行 ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub，核实 SHA256 指纹后添加信任。",
                    }),
                  )
                  .catch((e) => Message.error(e.message))
              }
            >
              查看复制公钥
            </Button>
            <Button
              onClick={() =>
                form(
                  "信任复制主机",
                  [
                    { key: "host", label: "主机 IP / DNS", required: true },
                    {
                      key: "port",
                      label: "SSH 端口",
                      type: "number",
                      max: 65535,
                      value: 22,
                    },
                    {
                      key: "fingerprint",
                      label: "已通过独立渠道核实的 SHA256 指纹",
                      required: true,
                    },
                  ],
                  async (v) => {
                    await api("/ssh/trust", "POST", v);
                    Message.success("主机指纹已验证并保存");
                  },
                )
              }
            >
              添加可信主机
            </Button>
            <Button
              type="primary"
              icon={<IconPlus />}
              onClick={() =>
                form(
                  "新建复制任务",
                  [
                    {
                      key: "repository_id",
                      label: "源仓库",
                      type: "select",
                      options: repoOptions(repos),
                      required: true,
                    },
                    { key: "host", label: "目标主机", required: true },
                    {
                      key: "user",
                      label: "SSH 用户",
                      value: "backup",
                      required: true,
                    },
                    {
                      key: "port",
                      label: "SSH 端口",
                      type: "number",
                      max: 65535,
                      value: 22,
                    },
                    {
                      key: "target_dataset",
                      label: "目标数据集",
                      value: "backup-pool/replicas/service",
                      required: true,
                      help: "目标父数据集须预先存在；不能与目标已有非备份数据混用。",
                    },
                    {
                      key: "cron",
                      label: "独立调度（可选）",
                      value: "0 3 * * *",
                    },
                    {
                      key: "after_snapshot",
                      label: "快照完成后自动复制",
                      type: "switch",
                      value: true,
                    },
                    {
                      key: "enabled",
                      label: "启用",
                      type: "switch",
                      value: true,
                    },
                  ],
                  async (v) => {
                    await api("/replications", "PUT", v);
                    Message.success("复制任务已保存");
                    refresh();
                  },
                )
              }
            >
              新建任务
            </Button>
          </div>
        )}
      </div>
      <ErrorBox error={reps.error} />
      <DataTable
        rows={reps.data}
        columns={[
          {
            title: "源仓库",
            dataIndex: "repository_id",
            render: (id: number) =>
              repos?.find((r) => r.repository.id === id)?.repository.name || id,
          },
          {
            title: "目标",
            render: (_: any, r: Row) => (
              <div>
                <strong>
                  {r.user}@{r.host}:{r.port}
                </strong>
                <small className="cell-sub">{r.target_dataset}</small>
              </div>
            ),
          },
          { title: "上次运行", dataIndex: "last_run_at", render: date },
          {
            title: "传输量 / 耗时",
            render: (_: any, r: Row) =>
              `${bytes(r.bytes)} / ${(r.duration_seconds || 0).toFixed(1)}s`,
          },
          {
            title: "状态",
            render: (_: any, r: Row) => (
              <div>
                <Tag
                  color={
                    r.last_error ? "red" : r.last_run_at ? "green" : "gray"
                  }
                >
                  {r.last_error
                    ? "上次失败"
                    : r.last_run_at
                      ? "已校验"
                      : "待首次运行"}
                </Tag>
                {r.last_error && (
                  <small className="cell-sub text-danger">{r.last_error}</small>
                )}
              </div>
            ),
          },
          {
            title: "操作",
            render: (_: any, r: Row) =>
              admin ? (
                <div className="row-actions">
                  <Button
                    type="text"
                    onClick={() => perform("replicate", { id: r.id })}
                  >
                    立即复制
                  </Button>
                  <Button
                    type="text"
                    onClick={async () => {
                      await api("/replications", "PUT", {
                        ...r,
                        enabled: !r.enabled,
                      });
                      refresh();
                    }}
                  >
                    {r.enabled ? "暂停" : "启用"}
                  </Button>
                  <Button
                    type="text"
                    status="danger"
                    onClick={() =>
                      form(
                        "删除复制任务",
                        [
                          {
                            key: "confirm",
                            label: "输入 DELETE 确认",
                            required: true,
                          },
                        ],
                        async (v) => {
                          if (v.confirm !== "DELETE")
                            throw new Error("确认文本不匹配");
                          await api("/replications/" + r.id, "DELETE");
                          refresh();
                        },
                        "删除调度配置并释放本机复制基线保护；不会删除远程数据。",
                      )
                    }
                  >
                    删除
                  </Button>
                </div>
              ) : null,
          },
        ]}
      />
    </>
  );
}
