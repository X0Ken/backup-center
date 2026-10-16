import DataSync from "./pages/DataSync";
import { currentPage } from "./navigation";
import Policies from "./pages/Policies";
import Repositories from "./pages/Repositories";
import Login from "./Login";
import Overview from "./pages/Overview";
import SnapshotPage from "./pages/SnapshotPage";
import Protocols from "./pages/Protocols";
import Replications from "./pages/Replications";
import Jobs from "./pages/Jobs";
import Monitor from "./pages/Monitor";
import System from "./pages/System";
import { kindLabels } from "./types";
import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Alert,
  Button,
  ConfigProvider,
  Form,
  Input,
  Message,
  Modal,
  Progress,
  Select,
  Spin,
  Tag,
} from "@arco-design/web-react";
import zhCN from "@arco-design/web-react/es/locale/zh-CN";
import {
  IconDashboard,
  IconStorage,
  IconCamera,
  IconLink,
  IconHistory,
  IconSync,
  IconSettings,
  IconList,
  IconSafe,
  IconRefresh,
  IconPlus,
  IconPoweroff,
  IconNotification,
  IconCheckCircle,
  IconFolder,
} from "@arco-design/web-react/icon";
import "@arco-design/web-react/dist/css/arco.css";
import "./style.css";
import { api, bytes, date, useAuth } from "./api";
import {
  Browser,
  Chart,
  DataTable,
  ErrorBox,
  Field,
  FormDialog,
  jobColumns,
  Row,
  Status,
  useData,
} from "./components";
const nav = [
  ["overview", "概览", IconDashboard],
  ["repositories", "备份仓库", IconStorage],
  ["snapshots", "快照", IconCamera],
  ["policies", "快照策略", IconSettings],
  ["protocols", "共享访问", IconLink],
  ["sync", "数据同步", IconSync],
  ["replication", "异地复制", IconSync],
  ["monitor", "监控告警", IconDashboard],
  ["jobs", "任务中心", IconList],
  ["system", "系统管理", IconSettings],
] as const;

function App() {
  const { user, set } = useAuth();
  const [checking, setChecking] = useState(true);
  const [page, setPage] = useState(currentPage);
  const [rev, setRev] = useState(0);
  const [dialog, setDialog] = useState<{
    title: string;
    fields: Field[];
    submit: (v: Row) => Promise<unknown>;
    warning?: string;
  } | null>(null);
  const [browser, setBrowser] = useState<Row | null>(null);
  const [text, setText] = useState<{ title: string; content: string } | null>(
    null,
  );
  const refresh = () => setRev((r) => r + 1);
  useEffect(() => {
    api("/auth/me")
      .then((r) => set(r.user, r.csrf))
      .catch(() => {})
      .finally(() => setChecking(false));
  }, []);
  useEffect(() => {
    const onHash = () => setPage(currentPage());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  useEffect(() => {
    if (!user) return;
    const es = new EventSource("/api/events");
    es.addEventListener("update", refresh);
    return () => es.close();
  }, [user?.id]);
  const form = (
    title: string,
    fields: Field[],
    submit: (v: Row) => Promise<unknown>,
    warning?: string,
  ) => setDialog({ title, fields, submit, warning });
  const perform = async (kind: string, payload: Row) => {
    const j = await api("/actions/" + kind, "POST", payload);
    Message.success(`任务 #${j.id} 已提交，请在任务中心确认结果`);
    refresh();
    return j;
  };

  const admin = user?.role === "admin";
  const repos = useData<Row[]>(user ? "/repositories" : "/auth/me", rev);
  const snaps = useData<Row[]>(user ? "/snapshots" : "/auth/me", rev);
  const overview = useData(user ? "/overview" : "/auth/me", rev);
  function changePassword() {
    form(
      "修改登录密码",
      [
        {
          key: "current_password",
          label: "当前密码",
          type: "password",
          required: true,
        },
        {
          key: "new_password",
          label: "新密码",
          type: "password",
          required: true,
          help: "至少 14 字节，最多 72 字节；建议使用密码管理器生成。",
        },
      ],
      async (v) => {
        await api("/auth/password", "POST", v);
        const me = await api("/auth/me");
        set(me.user, me.csrf);
        Message.success("密码已更新");
      },
    );
  }
  useEffect(() => {
    if (user?.must_change) changePassword();
  }, [user?.id, user?.must_change]);
  if (checking)
    return (
      <div className="loading">
        <Spin size={40} />
      </div>
    );
  if (!user) return <Login />;
  const title = nav.find((n) => n[0] === page)?.[1] || "概览";
  return (
    <div className="app">
      <aside>
        <a className="brand" href="#overview">
          <span className="brand-mark">
            <IconStorage />
          </span>
          <div>
            BACKUP CENTER<small>企业数据保护平台</small>
          </div>
        </a>
        <div className="nav-label">工作空间</div>
        <nav>
          {nav.map(([key, label, Icon]) => (
            <a
              key={key}
              className={page === key ? "active" : ""}
              href={"#" + key}
            >
              <Icon />
              {label}
              {key === "jobs" && overview.data?.active_jobs > 0 && (
                <b>{overview.data.active_jobs}</b>
              )}
            </a>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <IconSafe />
          <div>
            数据保护，持续在线<small>本地自治 · 企业级存储</small>
          </div>
        </div>
      </aside>
      <div className="workspace">
        <header>
          <span>
            工作空间 <span className="crumb">/ {title}</span>
          </span>
          <div className="header-actions">
            <button
              onClick={() => {
                location.hash = "monitor";
              }}
              className="header-icon"
              title="告警"
            >
              <IconNotification />
              {overview.data?.alerts > 0 && <b />}
            </button>
            <span className="avatar">{user.username[0].toUpperCase()}</span>
            <button className="user-button" onClick={changePassword}>
              {user.username}
              <small>{admin ? "管理员" : "操作员"}</small>
            </button>
            <Button
              type="text"
              icon={<IconPoweroff />}
              onClick={async () => {
                await api("/auth/logout", "POST");
                set(null);
              }}
              aria-label="退出登录"
            />
          </div>
        </header>
        <main>
          <div className="page-heading">
            <div>
              <div className="eyebrow">DATA PROTECTION WORKSPACE</div>
              <h1>{title}</h1>
              <p>
                {
                  (
                    {
                      overview: "掌握备份基础设施与数据保护任务的实时状态",
                      repositories:
                        "管理文件与块存储仓库，为业务数据建立独立的保护空间",
                      snapshots:
                        "管理快照，或基于快照创建新仓库并通过共享访问挂载查看内容",
                      policies:
                        "设置分层保留数量，系统自动计算快照频率并清理过期恢复点",
                      protocols:
                        "按仓库类型添加 NFS 或 iSCSI 访问方式，管理客户端权限与挂载入口",
                      sync: "通过 SSH 或 SFTP 持续拉取业务文件，完成一轮后发布有效版本，并结合快照策略保留恢复点",
                      replication:
                        "同版本平台间自动复制快照，支持后台传输、断点续传和失败重试",
                      monitor: "跟踪存储健康、容量变化与待处理告警",
                      jobs: "所有存储操作的执行状态与结果",
                      system: "管理账号、安全配置与设备信息",
                    } as Row
                  )[page]
                }
              </p>
            </div>
            <Button icon={<IconRefresh />} onClick={refresh}>
              刷新数据
            </Button>
          </div>
          {user.must_change && (
            <Alert
              type="warning"
              content="首次登录请先修改密码，完成后才能执行管理操作。"
              action={<Button onClick={changePassword}>修改密码</Button>}
            />
          )}
          {page === "overview" && (
            <Overview data={overview.data} error={overview.error} rev={rev} />
          )}
          {page === "repositories" && (
            <Repositories
              repos={repos}
              admin={admin}
              form={form}
              perform={perform}
              refresh={refresh}
              showText={setText}
            />
          )}

          {page === "snapshots" && (
            <SnapshotPage
              snaps={snaps}
              repos={repos.data}
              rev={rev}
              admin={admin}
              form={form}
              perform={perform}
              browse={setBrowser}
              refresh={refresh}
            />
          )}
          {page === "protocols" && (
            <Protocols
              rev={rev}
              repos={repos.data}
              admin={admin}
              form={form}
              refresh={refresh}
              showText={setText}
            />
          )}
          {page === "policies" && (
            <Policies
              repos={repos.data}
              rev={rev}
              admin={admin}
              form={form}
              perform={perform}
              refresh={refresh}
            />
          )}

          {page === "sync" && (
            <DataSync
              repos={repos.data}
              rev={rev}
              admin={admin}
              refresh={refresh}
            />
          )}
          {page === "replication" && (
            <Replications
              rev={rev}
              repos={repos.data}
              admin={admin}
              form={form}
              perform={perform}
              refresh={refresh}
              showText={setText}
            />
          )}
          {page === "monitor" && (
            <Monitor
              rev={rev}
              admin={admin}
              perform={perform}
              refresh={refresh}
            />
          )}
          {page === "jobs" && <Jobs rev={rev} />}
          {page === "system" && (
            <System
              rev={rev}
              admin={admin}
              form={form}
              refresh={refresh}
              showText={setText}
            />
          )}
        </main>
        <footer>
          <span>Backup Center</span>
          <span>独立备份 · 定期验证 · 从容恢复</span>
          <span>v{overview.data?.version || "1.4.0"}</span>
        </footer>
      </div>
      {dialog && (
        <FormDialog
          title={dialog.title}
          fields={dialog.fields}
          warning={dialog.warning}
          onSubmit={dialog.submit}
          onClose={() => setDialog(null)}
        />
      )}
      {browser && (
        <Browser snapshot={browser} onClose={() => setBrowser(null)} />
      )}
      {text && (
        <Modal
          visible
          title={text.title}
          onCancel={() => setText(null)}
          footer={
            <Button
              onClick={() =>
                navigator.clipboard
                  .writeText(text.content)
                  .then(() => Message.success("已复制"))
                  .catch(() => Message.error("复制失败，请手动复制"))
              }
            >
              复制内容
            </Button>
          }
          style={{ width: 900 }}
        >
          <pre className="code-block">{text.content}</pre>
        </Modal>
      )}
    </div>
  );
}
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ConfigProvider locale={zhCN}>
      <App />
    </ConfigProvider>
  </React.StrictMode>,
);
