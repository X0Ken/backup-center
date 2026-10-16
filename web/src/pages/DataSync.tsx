import React, { useState } from "react";
import {
  Alert,
  Button,
  Collapse,
  Form,
  Input,
  InputNumber,
  Message,
  Modal,
  Select,
  Switch,
  Tag,
} from "@arco-design/web-react";
import { api, bytes, date } from "../api";
import { DataTable, ErrorBox, Row, useData } from "../components";
import { Tools } from "../types";

function SyncEditor({
  row,
  repos,
  onClose,
  refresh,
}: {
  row?: Row;
  repos: Row[] | null;
  onClose: () => void;
  refresh: () => void;
}) {
  const initial = {
    name: row?.name || `sync-${Date.now()}`,
    repository_id: row?.repository_id,
    access_method: row?.access_method || "ssh",
    host: row?.host || "",
    port: row?.port || 22,
    username: row?.username || "",
    auth_method: row?.auth_method || "password",
    password: "",
    private_key: "",
    key_passphrase: "",
    source_path: row?.source_path || "",
    fingerprint: row?.fingerprint || "",
    bandwidth_kibps: row?.bandwidth_kibps || 0,
    interval_seconds: row?.interval_seconds || 60,
    max_delete_percent: row?.max_delete_percent || 20,
    allow_empty: row?.allow_empty || false,
    enabled: row?.enabled ?? true,
  };
  const [form] = Form.useForm();
  const [values, setValues] = useState<Row>(initial);
  const [probe, setProbe] = useState<Row | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const connection = async (path?: string, fingerprint?: string) => {
    setBusy(true);
    setError("");
    try {
      const q: Row = { ...initial, ...form.getFieldsValue(), id: row?.id || 0 };
      if (path !== undefined) {
        q.source_path = path;
        form.setFieldValue("source_path", path);
      }
      if (fingerprint !== undefined) {
        q.fingerprint = fingerprint;
        form.setFieldValue("fingerprint", fingerprint);
      }
      const p = await api("/sync/probe", "POST", q);
      setProbe(p);
      if (!p.needs_trust) Message.success("连接及源目录检查通过");
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      visible
      title={row ? "编辑数据同步" : "添加数据同步"}
      style={{ width: 760 }}
      okText="保存"
      cancelText="取消"
      confirmLoading={busy}
      onCancel={() => !busy && onClose()}
      onOk={async () => {
        try {
          const q = { ...initial, ...(await form.validate()) };
          setBusy(true);
          setError("");
          await api("/sync/tasks", "PUT", { ...q, id: row?.id || 0 });
          Message.success("数据同步任务已保存");
          refresh();
          onClose();
        } catch (e: any) {
          setError(e.message || "请检查表单并测试连接");
        } finally {
          setBusy(false);
        }
      }}
    >
      <ErrorBox error={error} />
      <Form
        form={form}
        layout="vertical"
        initialValues={initial}
        onValuesChange={(changed, all) => {
          if ("host" in changed || "port" in changed) {
            form.setFieldValue("fingerprint", "");
            setProbe(null);
          }
          setValues(all);
        }}
      >
        <Form.Item
          field="name"
          label="任务名称"
          rules={[{ required: true }]}
          extra="以字母开头，可使用数字、下划线和连字符。"
        >
          <Input />
        </Form.Item>
        <Form.Item field="access_method" label="访问方式">
          <Select
            aria-label="访问方式"
            disabled={!!row}
            options={[
              { label: "SSH · Linux 目录备份（源端需要 rsync）", value: "ssh" },
              {
                label: "SFTP · 普通文件采集（源端无需安装软件）",
                value: "sftp",
              },
            ]}
          />
        </Form.Item>
        <Alert
          type="info"
          style={{ marginBottom: 16 }}
          content={
            values.access_method === "ssh"
              ? "平台自动使用 rsync，保留 Linux 权限、属主、ACL、扩展属性和硬链接；源端需要相应读取权限。"
              : "平台自动使用 rclone，通过现有 SFTP 服务采集普通文件。Unix 属主、ACL、硬链接及符号链接不作为恢复保证。"
          }
        />
        <div className="two-cols">
          <Form.Item
            field="host"
            label="源主机地址"
            rules={[{ required: true }]}
          >
            <Input disabled={!!row} />
          </Form.Item>
          <Form.Item field="port" label="SSH 端口">
            <InputNumber min={1} max={65535} disabled={!!row} />
          </Form.Item>
        </div>
        <Form.Item
          field="username"
          label="登录用户名"
          rules={[{ required: true }]}
        >
          <Input disabled={!!row} />
        </Form.Item>
        <Form.Item field="auth_method" label="认证方式">
          <Select
            aria-label="认证方式"
            options={[
              { label: "密码", value: "password" },
              { label: "SSH 私钥", value: "key" },
            ]}
          />
        </Form.Item>
        {values.auth_method === "password" ? (
          <Form.Item
            field="password"
            label="登录密码"
            extra={
              row
                ? "留空使用已保存的凭证。"
                : "凭证加密保存在平台，日志和列表不展示密码。"
            }
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        ) : (
          <>
            <Form.Item
              field="private_key"
              label="SSH 私钥"
              extra={
                row
                  ? "留空使用已保存的私钥。"
                  : "支持 OpenSSH / PEM 私钥，平台加密保存。"
              }
            >
              <Input.TextArea autoSize={{ minRows: 3, maxRows: 6 }} />
            </Form.Item>
            <Form.Item field="key_passphrase" label="私钥口令（可选）">
              <Input.Password autoComplete="new-password" />
            </Form.Item>
          </>
        )}
        <Form.Item
          field="source_path"
          label="源目录"
          rules={[{ required: true }]}
          extra="选择具体业务数据或业务导出的备份目录。在线数据库数据目录应使用数据库备份工具先生成一致性副本。"
        >
          <Input disabled={!!row} />
        </Form.Item>
        <Form.Item field="fingerprint" label="已绑定主机指纹">
          <Input readOnly placeholder="测试连接后确认主机指纹" />
        </Form.Item>
        <Button loading={busy} onClick={() => connection()}>
          测试连接与浏览目录
        </Button>
        <Button
          type="text"
          disabled={busy}
          onClick={() => connection(undefined, "")}
        >
          重新核对主机指纹
        </Button>
        {probe?.needs_trust && (
          <Alert
            style={{ marginTop: 12 }}
            type="warning"
            content={
              <div>
                <p>首次连接，请核对源主机的 SSH 指纹：</p>
                <code>{probe.fingerprint}</code>
                <p>
                  <Button
                    onClick={() => connection(undefined, probe.fingerprint)}
                  >
                    信任此指纹并测试连接
                  </Button>
                </p>
              </div>
            }
          />
        )}
        {probe && !probe.needs_trust && (
          <div className="sync-browser">
            <strong>已连接 · {probe.path}</strong>
            {!row && (
              <>
                <Button
                  type="text"
                  disabled={probe.path === "/"}
                  onClick={() => connection(probe.parent)}
                >
                  上一级
                </Button>
                <div className="sync-directories">
                  {probe.directories.map((d: Row) => (
                    <Button key={d.path} onClick={() => connection(d.path)}>
                      {d.name}/
                    </Button>
                  ))}
                </div>
                {probe.truncated && (
                  <p>仅展示前 500 个目录，也可直接填写完整路径。</p>
                )}
              </>
            )}
          </div>
        )}
        <Form.Item
          field="repository_id"
          label="备份到仓库"
          rules={[{ required: true }]}
          extra="需要空的专用文件仓库，每个仓库只能绑定一个同步任务；共享访问仅允许只读。已有快照策略保持不变，尚无策略时自动启用默认分层保留，可到快照策略页修改。"
        >
          <Select
            aria-label="备份到仓库"
            disabled={!!row}
            options={(repos || [])
              .filter((r) => r.repository.kind === "filesystem")
              .map((r) => ({
                label: r.repository.name,
                value: r.repository.id,
              }))}
          />
        </Form.Item>
        <Collapse style={{ marginBottom: 16 }}>
          <Collapse.Item header="高级设置" name="advanced">
            <Form.Item
              field="interval_seconds"
              label="同步间隔（秒）"
              extra="默认每分钟检查；运行中不重叠执行。实际延迟取决于扫描和传输耗时。"
            >
              <InputNumber min={60} max={86400} />
            </Form.Item>
            <Form.Item
              field="bandwidth_kibps"
              label="传输限速（KiB/s）"
              extra="0 表示不限制，可用于控制备份对业务网络的占用。"
            >
              <InputNumber min={0} max={1048576} />
            </Form.Item>
            <Form.Item
              field="max_delete_percent"
              label="最大删除比例（%）"
              extra="本轮文件删除比例超过此值时不发布；核对源端后可调整。"
            >
              <InputNumber min={1} max={100} />
            </Form.Item>
            <Form.Item
              field="allow_empty"
              label="允许发布空目录"
              triggerPropName="checked"
            >
              <Switch />
            </Form.Item>
          </Collapse.Item>
        </Collapse>
        <Form.Item
          field="enabled"
          label="启用自动同步"
          triggerPropName="checked"
        >
          <Switch />
        </Form.Item>
      </Form>
    </Modal>
  );
}
export default function DataSync({
  repos,
  rev,
  admin,
  refresh,
}: Pick<Tools, "repos" | "rev" | "admin" | "refresh">) {
  const data = useData<Row[]>("/sync/tasks", rev);
  const [editor, setEditor] = useState<Row | undefined | null>(null);
  const control = async (id: number, operation: string) => {
    try {
      const r = await api(`/sync/tasks/${id}/${operation}`, "POST", {});
      Message.success(
        operation === "run" ? `同步任务 #${r.id} 已提交` : "同步设置已更新",
      );
      refresh();
    } catch (e: any) {
      Message.error(e.message);
    }
  };
  const phases: Row = {
    preflight: "检查连接",
    preparing: "准备工作副本",
    transferring: "传输中",
    validating: "校验中",
    publishing: "发布中",
    completed: "已完成",
    failed: "失败",
    interrupted: "已停止",
  };
  return (
    <>
      <div className="section-toolbar">
        <span>单向拉取数据，成功版本结合现有快照策略保留</span>
        {admin && (
          <Button type="primary" onClick={() => setEditor(undefined)}>
            添加数据同步
          </Button>
        )}
      </div>
      <ErrorBox error={data.error} />
      <DataTable
        rows={data.data}
        tableName="数据同步"
        searchFields={[
          { key: "task.name", label: "任务名称" },
          { key: "task.host", label: "源主机" },
          { key: "task.source_path", label: "源目录" },
          {
            key: "repository",
            label: "目标仓库",
            value: (r) =>
              repos?.find((x) => x.repository.id === r.task.repository_id)
                ?.repository.name,
          },
          { key: "task.access_method", label: "访问方式" },
        ]}
        searchText={(r) =>
          repos?.find((x) => x.repository.id === r.task.repository_id)
            ?.repository.name || ""
        }
        rowKey="task.id"
        columns={[
          {
            title: "任务 / 来源",
            render: (_: any, r: Row) => (
              <div>
                <strong>{r.task.name}</strong>
                <small className="cell-sub">
                  {r.task.username}@{r.task.host}:{r.task.port}
                  {r.task.source_path}
                </small>
                <Tag>
                  {r.task.access_method === "ssh"
                    ? "SSH · rsync"
                    : "SFTP · rclone"}
                </Tag>
              </div>
            ),
          },
          {
            title: "目标仓库",
            render: (_: any, r: Row) => (
              <Button
                type="text"
                onClick={() => {
                  location.hash = `snapshots?repository_id=${r.task.repository_id}`;
                }}
              >
                {repos?.find((x) => x.repository.id === r.task.repository_id)
                  ?.repository.name || r.task.repository_id}
              </Button>
            ),
          },
          {
            title: "同步状态",
            render: (_: any, r: Row) => (
              <div>
                <Tag
                  color={
                    r.run.status === "running"
                      ? "arcoblue"
                      : r.task.last_error
                        ? "red"
                        : r.task.enabled
                          ? "green"
                          : "gray"
                  }
                >
                  {r.run.status === "running"
                    ? phases[r.run.phase] || "运行中"
                    : r.task.last_error
                      ? "需要处理"
                      : r.task.enabled
                        ? "自动同步已启用"
                        : "自动同步已暂停"}
                </Tag>
                <small className="cell-sub">
                  {r.task.last_error ||
                    (r.task.enabled
                      ? `下次检查 ${date(r.task.next_run_at)}`
                      : "可手动立即同步")}
                </small>
              </div>
            ),
          },
          {
            title: "最近成功 / 延迟",
            render: (_: any, r: Row) => (
              <div>
                {r.task.last_success_at
                  ? date(r.task.last_success_at)
                  : "尚无成功版本"}
                <small className="cell-sub">
                  {r.task.last_success_at
                    ? `距最近成功 ${Math.max(0, Math.floor((Date.now() - new Date(r.task.last_success_at).getTime()) / 60000))} 分钟`
                    : "首次成功后自动建立基线快照"}
                </small>
              </div>
            ),
          },
          {
            title: "最近一轮",
            render: (_: any, r: Row) => (
              <span>
                {r.run.id
                  ? `${r.run.files} 个文件 · ${bytes(r.run.bytes)}`
                  : "—"}
                <small className="cell-sub">
                  {r.run.id
                    ? `传输 ${bytes(r.run.transferred_bytes)} · 删除 ${r.run.deleted_files} 个文件`
                    : ""}
                </small>
              </span>
            ),
          },
          {
            title: "操作",
            width: 260,
            render: (_: any, r: Row) => (
              <div className="row-actions">
                {admin && (
                  <>
                    <Button
                      type="text"
                      disabled={r.run.status === "running"}
                      onClick={() => control(r.task.id, "run")}
                    >
                      立即同步
                    </Button>
                    <Button
                      type="text"
                      onClick={() =>
                        control(r.task.id, r.task.enabled ? "pause" : "resume")
                      }
                    >
                      {r.task.enabled ? "暂停" : "启用"}
                    </Button>
                    {r.run.status === "running" ? (
                      <Button
                        type="text"
                        onClick={() => control(r.task.id, "stop")}
                      >
                        停止本轮
                      </Button>
                    ) : (
                      <Button type="text" onClick={() => setEditor(r.task)}>
                        编辑
                      </Button>
                    )}
                  </>
                )}
                <Button
                  type="text"
                  onClick={() =>
                    Modal.info({
                      title: "同步运行详情",
                      style: { width: 900 },
                      content: (
                        <div>
                          <p>
                            {phases[r.run.phase] || "尚未运行"} ·{" "}
                            {r.run.generation || "尚未发布"}
                          </p>
                          <p className="text-danger">{r.run.error}</p>
                          <pre className="code-block">
                            {r.run.log || "暂无运行日志"}
                          </pre>
                        </div>
                      ),
                    })
                  }
                >
                  详情
                </Button>
                {admin && !r.task.enabled && r.run.status !== "running" && (
                  <Button
                    type="text"
                    status="danger"
                    onClick={() =>
                      Modal.confirm({
                        title: "删除同步任务配置",
                        content: "仓库、已发布数据和快照将保留。",
                        onOk: async () => {
                          try {
                            await api(`/sync/tasks/${r.task.id}`, "DELETE");
                            refresh();
                          } catch (e: any) {
                            Message.error(e.message);
                            throw e;
                          }
                        },
                      })
                    }
                  >
                    删除配置
                  </Button>
                )}
              </div>
            ),
          },
        ]}
      />
      <section>
        <div className="section-title">
          <h2>同步运行历史</h2>
          <span className="muted">
            未完成版本不会替换 current；快照保留规则在快照策略中管理
          </span>
        </div>
        <DataTable
          rows={null}
          endpoint="/sync/runs"
          revision={rev}
          tableName="同步历史"
          columns={[
            {
              title: "任务",
              dataIndex: "task_id",
              render: (id: number) =>
                data.data?.find((r) => r.task.id === id)?.task.name || id,
            },
            {
              title: "状态",
              dataIndex: "phase",
              render: (v: string) => phases[v] || v,
            },
            { title: "开始时间", dataIndex: "started_at", render: date },
            { title: "文件数", dataIndex: "files" },
            { title: "副本大小", dataIndex: "bytes", render: bytes },
            { title: "传输量", dataIndex: "transferred_bytes", render: bytes },
            {
              title: "结果",
              render: (_: any, r: Row) => r.error || r.generation || "—",
            },
          ]}
        />
      </section>
      {editor !== null && (
        <SyncEditor
          row={editor}
          repos={repos}
          onClose={() => setEditor(null)}
          refresh={refresh}
        />
      )}
    </>
  );
}
