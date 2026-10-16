import React, { useState } from "react";
import {
  Button,
  Input,
  Message,
  Modal,
  Select,
  Tag,
} from "@arco-design/web-react";
import { IconPlus } from "@arco-design/web-react/icon";
import { api, bytes, date } from "../api";
import { DataTable, ErrorBox, Row, useData } from "../components";
import { Tools } from "../types";

const states: Row = {
  provisioning: "创建远端仓库",
  waiting: "等待快照",
  sending: "正在传输",
  retrying: "等待续传重试",
  ready: "已校验",
  legacy: "需重新配置",
};
export default function Replications({
  rev,
  repos,
  admin,
  perform,
  refresh,
}: Tools) {
  const reps = useData<Row[]>("/replications", rev);
  const peers = useData<Row[]>(admin ? "/peers" : "", rev);
  const target = peers.data?.find((p) => p.direction === "outbound");
  const [open, setOpen] = useState(false);
  const [repository, setRepository] = useState<number>();
  const [name, setName] = useState("");
  const [custom, setCustom] = useState(false);
  const [saving, setSaving] = useState(false);
  const sourceName = (id?: number) => {
    const r = repos?.find((r) => r.repository.id === id)?.repository;
    return r?.display_name || r?.name || "";
  };
  return (
    <>
      <div className="section-toolbar">
        <span>
          {target
            ? `目标平台：${target.name || target.url}`
            : "请先在系统管理中配置异地目标平台"}
        </span>
        {admin && (
          <Button
            type="primary"
            icon={<IconPlus />}
            disabled={!target}
            onClick={() => {
              setRepository(undefined);
              setName("");
              setCustom(false);
              setOpen(true);
            }}
          >
            新建任务
          </Button>
        )}
      </div>
      <ErrorBox error={reps.error || peers.error} />
      <DataTable
        rows={reps.data}
        columns={[
          {
            title: "源仓库",
            render: (_: any, r: Row) =>
              sourceName(r.repository_id) || r.repository_id,
          },
          {
            title: "目标仓库",
            render: (_: any, r: Row) => (
              <div>
                <strong>{r.remote_name || r.target_dataset}</strong>
                <small className="cell-sub">
                  {r.remote_repository_id
                    ? `远端仓库 ID：${r.remote_repository_id}`
                    : "后台自动登记"}
                </small>
              </div>
            ),
          },
          {
            title: "状态",
            render: (_: any, r: Row) => (
              <div>
                <Tag
                  color={
                    !r.enabled
                      ? "gray"
                      : r.state === "ready"
                        ? "green"
                        : r.last_error
                          ? "orange"
                          : "blue"
                  }
                >
                  {!r.enabled ? "已暂停" : states[r.state] || "排队中"}
                </Tag>
                {r.last_error && (
                  <small className="cell-sub text-danger">{r.last_error}</small>
                )}
                {r.state === "retrying" && r.enabled && (
                  <small className="cell-sub">
                    下次重试：{date(r.next_attempt_at)}
                  </small>
                )}
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
            title: "操作",
            render: (_: any, r: Row) =>
              admin && (
                <div className="row-actions">
                  {r.peer_id > 0 && (
                    <>
                      <Button
                        type="text"
                        disabled={!r.enabled}
                        onClick={() => perform("replicate", { id: r.id })}
                      >
                        立即复制
                      </Button>
                      <Button
                        type="text"
                        onClick={async () => {
                          try {
                            await api("/replications", "PUT", {
                              id: r.id,
                              enabled: !r.enabled,
                            });
                            refresh();
                          } catch (e: any) {
                            Message.error(e.message);
                          }
                        }}
                      >
                        {r.enabled ? "暂停" : "启用"}
                      </Button>
                    </>
                  )}
                  <Button
                    type="text"
                    status="danger"
                    onClick={() =>
                      Modal.confirm({
                        title: "删除复制任务",
                        content:
                          "仅删除任务配置并释放本机基线保护，远端仓库和数据会保留。请先暂停并等待当前传输结束。",
                        onOk: async () => {
                          await api("/replications/" + r.id, "DELETE");
                          refresh();
                        },
                      })
                    }
                  >
                    删除
                  </Button>
                </div>
              ),
          },
        ]}
      />
      <Modal
        title="新建异地复制任务"
        visible={open}
        confirmLoading={saving}
        onCancel={() => setOpen(false)}
        onOk={async () => {
          if (!repository) {
            Message.error("请选择源仓库");
            return;
          }
          setSaving(true);
          try {
            await api("/replications", "PUT", {
              repository_id: repository,
              remote_name: name,
            });
            Message.success("任务已提交，正在后台创建远端仓库");
            setOpen(false);
            refresh();
          } catch (e: any) {
            Message.error(e.message);
          } finally {
            setSaving(false);
          }
        }}
      >
        <label htmlFor="replica-source">需要备份的仓库</label>
        <Select
          id="replica-source"
          aria-label="需要备份的仓库"
          style={{ margin: "8px 0 20px" }}
          value={repository}
          placeholder="选择本机仓库"
          options={(repos || [])
            .filter((r) => !r.repository.replica)
            .map((r) => ({
              label: r.repository.display_name || r.repository.name,
              value: r.repository.id,
            }))}
          onChange={(id) => {
            setRepository(id);
            if (!custom) setName("【异地复制】" + sourceName(id));
          }}
        />
        <label htmlFor="replica-name">目标仓库名称</label>
        <Input
          id="replica-name"
          aria-label="目标仓库名称"
          style={{ margin: "8px 0" }}
          maxLength={128}
          value={name}
          onChange={(v) => {
            setName(v);
            setCustom(true);
          }}
          placeholder="选择仓库后自动生成，可修改"
        />
        <p className="muted">
          新快照自动触发异步复制。中断后保留接收状态并自动重试，无需额外调度。
        </p>
      </Modal>
    </>
  );
}
