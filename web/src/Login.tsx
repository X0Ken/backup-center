import React, { useState } from "react";
import { Alert, Button, Form, Input } from "@arco-design/web-react";
import { IconStorage, IconSafe } from "@arco-design/web-react/icon";
import { api, useAuth } from "./api";
export default function Login() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const set = useAuth((s) => s.set);
  return (
    <div className="login">
      <div className="login-art">
        <div className="brand-light">
          <IconStorage /> BACKUP CENTER
        </div>
        <div>
          <span className="eyebrow">YOUR DATA. ALWAYS WITH YOU.</span>
          <h1>
            让每一份数据，
            <br />
            都有可靠的归处。
          </h1>
          <p>ZFS 数据保护 · 快照恢复 · 异地复制</p>
          <div className="login-cubes">
            <span />
            <span />
            <span />
          </div>
        </div>
        <small>企业备份与恢复管理平台</small>
      </div>
      <div className="login-side">
        <div className="login-box">
          <span className="eyebrow">SECURE ACCESS</span>
          <h2>登录备份中心</h2>
          <p className="muted">使用管理员或操作员账号访问设备</p>
          {error && <Alert type="error" content={error} />}
          <Form
            layout="vertical"
            onSubmit={async (values) => {
              setBusy(true);
              try {
                const r = await api("/auth/login", "POST", values);
                set(r.user, r.csrf);
              } catch (e: any) {
                setError(e.message);
              } finally {
                setBusy(false);
              }
            }}
          >
            <Form.Item
              field="username"
              label="用户名"
              rules={[{ required: true }]}
            >
              <Input autoComplete="username" placeholder="输入用户名" />
            </Form.Item>
            <Form.Item
              field="password"
              label="密码"
              rules={[{ required: true }]}
            >
              <Input.Password
                autoComplete="current-password"
                placeholder="输入密码"
              />
            </Form.Item>
            <Button long type="primary" htmlType="submit" loading={busy}>
              登录
            </Button>
          </Form>
          <div className="login-note">
            <IconSafe /> 加密连接 · 基于角色的访问控制
          </div>
        </div>
      </div>
    </div>
  );
}
