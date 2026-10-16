import { test, expect } from "@playwright/test";

test("snapshot, policy, access, audit and disks follow the repository model", async ({
  page,
}) => {
  test.setTimeout(45000);
  page.setDefaultTimeout(10000);
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const writes: { path: string; body: any }[] = [];
  const repos = [
    {
      repository: { id: 101, name: "files", kind: "filesystem" },
      storage: { encryption: "off", compression: "lz4" },
    },
    {
      repository: { id: 102, name: "blocks", kind: "volume" },
      storage: { encryption: "off", compression: "lz4" },
    },
  ];
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api", "");
    if (path === "/events") {
      await route.abort();
      return;
    }
    if (route.request().method() !== "GET") {
      writes.push({ path, body: route.request().postDataJSON() });
      await route.fulfill({ json: { id: 77 } });
      return;
    }
    const payload: Record<string, any> = {
      "/auth/me": {
        user: { id: 1, username: "tester", role: "admin", must_change: false },
        csrf: "csrf",
      },
      "/repositories": repos,
      "/snapshots": [
        {
          repository_id: 101,
          name: "tank/files@bc-test",
          created_at: "2026-10-15T09:00:00Z",
          clones: "-",
          holds: 0,
        },
      ],
      "/policies": [],
      "/exports": [],
      "/connections": {},
      "/overview": {},
      "/replications": [],
      "/users": [],
      "/settings": {},
      "/audit": [
        {
          id: 1,
          action: "repository.create.succeeded",
          detail: "",
          username: "tester",
        },
        { id: 2, action: "auth.login.failed", detail: "", username: "tester" },
      ],
      "/system": {
        version: "1.2.0",
        network: "[]",
        disks: [
          {
            name: "/dev/nvme0n1",
            role: "备份数据盘",
            model: "INTEL",
            serial: "disk-serial",
            size: 3200000000000,
            transport: "nvme",
            media: "SSD",
            mountpoints: [],
            pool_state: "ONLINE",
          },
        ],
      },
    };
    await route.fulfill({ json: payload[path] ?? [] });
  });
  await page.goto("/#snapshots");
  await expect(
    page.getByRole("heading", { name: "快照", exact: true }),
  ).toBeVisible();
  await expect(
    page.locator("nav").getByText("数据恢复", { exact: true }),
  ).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "自动快照策略" })).toHaveCount(
    0,
  );
  await page.getByRole("button", { name: "创建仓库", exact: true }).click();
  await page.getByLabel("新仓库名称", { exact: true }).fill("from-snapshot");
  await page.getByRole("button", { name: "创建", exact: true }).click();
  expect(writes.at(-1)).toEqual({
    path: "/actions/restore.clone",
    body: {
      repository_id: 101,
      snapshot: "tank/files@bc-test",
      name: "from-snapshot",
    },
  });

  await page.locator("nav").getByText("快照策略", { exact: true }).click();
  await page.getByRole("button", { name: "添加快照策略", exact: true }).click();
  await expect(page.getByText(/Cron/)).toHaveCount(0);
  await page.getByLabel("仓库", { exact: true }).click();
  await page.getByRole("option", { name: "files", exact: true }).click();
  await page.getByLabel("小时级恢复点", { exact: true }).fill("0");
  await expect(
    page.getByText("自动快照频率：每天零点（服务器时区）"),
  ).toBeVisible();
  await page.getByRole("button", { name: "保存", exact: true }).click();
  expect(writes.at(-1)?.body.cron).toBeUndefined();
  expect(writes.at(-1)?.body.keep_hourly).toBe(0);

  await page.locator("nav").getByText("共享访问", { exact: true }).click();
  await page.getByRole("button", { name: "添加访问方式", exact: true }).click();
  await page.getByLabel("仓库", { exact: true }).click();
  await page.getByRole("option", { name: "files · NFS", exact: true }).click();
  await expect(
    page.getByLabel("允许访问的客户端 IP / CIDR", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByLabel("允许访问的客户端 IQN", { exact: true }),
  ).toHaveCount(0);
  await page
    .getByLabel("允许访问的客户端 IP / CIDR", { exact: true })
    .fill("198.51.100.0/24");
  await page.getByLabel("仓库", { exact: true }).click();
  await page
    .getByRole("option", { name: "blocks · iSCSI", exact: true })
    .click();
  await expect(
    page.getByLabel("允许访问的客户端 IP / CIDR", { exact: true }),
  ).toHaveCount(0);
  await page
    .getByLabel("允许访问的客户端 IQN", { exact: true })
    .fill("iqn.2026-10.local.client:test");
  await page.getByRole("button", { name: "保存", exact: true }).click();
  expect(writes.at(-1)?.body.repository_id).toBe(102);
  expect(writes.at(-1)?.body.clients).toBeUndefined();
  expect(writes.at(-1)?.body.protocol).toBeUndefined();
  expect(writes.at(-1)?.body.iqn).toBeUndefined();
  await expect(
    page.getByLabel("目标端 IQN（可选）", { exact: true }),
  ).toHaveCount(0);

  await page.locator("nav").getByText("备份仓库", { exact: true }).click();
  await page.getByRole("button", { name: "创建仓库", exact: true }).click();
  await page.getByLabel("仓库类型", { exact: true }).click();
  await page
    .getByRole("option", { name: "块卷仓库 · iSCSI", exact: true })
    .click();
  await expect(page.getByLabel("卷容量", { exact: true })).toBeVisible();
  await expect(page.getByLabel("容量配额", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("文件记录大小", { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "取消", exact: true }).click();

  await page.locator("nav").getByText("异地复制", { exact: true }).click();
  await expect(page.locator(".guidance")).toHaveCount(0);
  await expect(page.locator(".page-heading p")).toContainText("断点续传");
  await page.locator("nav").getByText("系统管理", { exact: true }).click();
  await expect(
    page.getByRole("cell", { name: "/dev/nvme0n1 备份数据盘", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("创建仓库 · 已完成", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("登录失败", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("repository shortcuts preserve filters, pagination totals and fixed policy ownership", async ({
  page,
}) => {
  page.setDefaultTimeout(10000);
  const writes: any[] = [];
  const repositories = Array.from({ length: 25 }, (_, i) => ({
    repository: {
      id: i + 1,
      name: `repo-${String(i + 1).padStart(2, "0")}`,
      kind: "filesystem",
    },
    storage: { encryption: "off", compression: "lz4", key_status: "available" },
    policy: {
      id: i + 1,
      repository_id: i + 1,
      keep_hourly: 7,
      keep_daily: 14,
      keep_weekly: 0,
      keep_monthly: 0,
      keep_yearly: 0,
      enabled: true,
    },
  }));
  const snapshots = repositories.flatMap((r) =>
    [1, 2].map((n) => ({
      repository_id: r.repository.id,
      name: `tank/${r.repository.name}@bc-${n}`,
      created_at: "2026-10-16T01:00:00Z",
      clones: "-",
      holds: 0,
    })),
  );
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api", "");
    if (path === "/events") {
      await route.abort();
      return;
    }
    if (route.request().method() !== "GET") {
      writes.push(route.request().postDataJSON());
      await route.fulfill({ json: { id: 1 } });
      return;
    }
    const payload: Record<string, any> = {
      "/auth/me": {
        user: { id: 1, username: "tester", role: "admin", must_change: false },
        csrf: "csrf",
      },
      "/repositories": repositories,
      "/snapshots": snapshots,
      "/policies": repositories.map((r) => ({
        ...r.policy,
        schedule_label: "每小时整点",
        timezone: "Asia/Shanghai",
      })),
      "/overview": {
        snapshots: 50,
        repositories: 25,
        pools: [],
        recent_jobs: [],
      },
    };
    await route.fulfill({ json: payload[path] || [] });
  });
  await page.goto("/#overview");
  await expect(
    page
      .locator(".stat-card")
      .filter({ hasText: "快照总数" })
      .locator("strong"),
  ).toHaveText("50");
  await page.locator("nav").getByText("备份仓库", { exact: true }).click();
  const table = page.getByLabel("仓库表格", { exact: true });
  await expect(table.getByRole("status")).toHaveText("共 25 条 · 第 1 / 3 页");
  await expect(table.locator("tbody tr")).toHaveCount(10);
  await table.locator(".arco-pagination-item-next").click();
  await expect(table.getByRole("status")).toHaveText("共 25 条 · 第 2 / 3 页");
  await page.getByLabel("筛选仓库", { exact: true }).fill("repo-24");
  await expect(table.getByRole("status")).toHaveText(
    "共 1 条（筛选前 25 条） · 第 1 / 1 页",
  );
  const row = table.locator("tbody tr");
  await expect(row.getByRole("button")).toHaveCount(3);
  await row.getByRole("button", { name: "更多操作", exact: true }).click();
  await page.getByRole("button", { name: "编辑备份策略", exact: true }).click();
  await expect(
    page.getByRole("dialog", { name: "编辑快照策略" }),
  ).toBeVisible();
  await expect(page.getByLabel("小时级恢复点", { exact: true })).toHaveValue(
    "7",
  );
  await page.getByRole("button", { name: "保存", exact: true }).click();
  expect(writes.at(-1).repository_id).toBe(24);
  await page.getByRole("heading", { name: "备份仓库", exact: true }).click();
  await row.getByRole("button", { name: "查看快照", exact: true }).click();
  await expect(page).toHaveURL(/#snapshots\?repository_id=24$/);
  await expect(page.getByLabel("按仓库筛选快照", { exact: true })).toHaveCount(
    0,
  );
  const snaps = page.getByLabel("快照表格", { exact: true });
  await expect(snaps.getByRole("status")).toHaveText("共 2 条 · 第 1 / 1 页");
  await expect(snaps.locator("tbody tr")).toHaveCount(2);
  await expect(snaps.locator("tbody")).toContainText("repo-24@bc-");
  await page.evaluate(() => {
    location.hash = "snapshots?repository_id=25";
  });
  await expect(snaps.locator("tbody")).toContainText("repo-25@bc-");
  await expect(snaps.locator("tbody")).not.toContainText("repo-24@bc-");
  await page.reload();
  await expect(snaps.getByRole("status")).toHaveText("共 2 条 · 第 1 / 1 页");
  await page.getByRole("button", { name: "显示全部仓库", exact: true }).click();
  await expect(page).toHaveURL(/#snapshots$/);
  await expect(snaps.getByRole("status")).toHaveText("共 50 条 · 第 1 / 5 页");
  await page.getByLabel("筛选快照", { exact: true }).fill("repo-25");
  await expect(snaps.getByRole("status")).toHaveText(
    "共 2 条（筛选前 50 条） · 第 1 / 1 页",
  );
  await page.evaluate(() => {
    location.hash = "policies?repository_id=24";
  });
  await expect(
    page.getByLabel("快照策略表格", { exact: true }).getByRole("status"),
  ).toHaveText("共 1 条 · 第 1 / 1 页");
  await expect(page.getByLabel("按仓库筛选策略", { exact: true })).toHaveCount(
    0,
  );
  await page.getByRole("button", { name: "显示全部仓库", exact: true }).click();
  const policies = page.getByLabel("快照策略表格", { exact: true });
  await expect(policies.getByRole("status")).toHaveText(
    "共 25 条 · 第 1 / 3 页",
  );
  await page.getByLabel("筛选快照策略", { exact: true }).fill("repo-24");
  await expect(policies.getByRole("status")).toHaveText(
    "共 1 条（筛选前 25 条） · 第 1 / 1 页",
  );
});

test("unified data sync handles host trust and engine-specific credentials", async ({
  page,
}) => {
  page.setDefaultTimeout(10000);
  const writes: any[] = [];
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api", "");
    if (path === "/events") {
      await route.abort();
      return;
    }
    const method = route.request().method();
    if (method !== "GET") {
      const body = route.request().postDataJSON();
      writes.push({ path, body });
      if (path === "/sync/probe")
        await route.fulfill({
          json: body.fingerprint
            ? {
                needs_trust: false,
                fingerprint: body.fingerprint,
                path: "/data",
                parent: "/",
                directories: [],
                engine: "rclone",
              }
            : { needs_trust: true, fingerprint: "SHA256:test-fingerprint" },
        });
      else await route.fulfill({ json: { id: 1 } });
      return;
    }
    const payload: Record<string, any> = {
      "/auth/me": {
        user: { id: 1, username: "tester", role: "admin", must_change: false },
        csrf: "csrf",
      },
      "/repositories": [
        {
          repository: { id: 100, name: "sync-repo", kind: "filesystem" },
          storage: {},
        },
      ],
      "/sync/tasks": [],
      "/sync/runs": { items: [], total: 0 },
      "/overview": {},
    };
    await route.fulfill({ json: payload[path] || [] });
  });
  await page.goto("/#sync");
  await expect(
    page.getByRole("heading", { name: "数据同步", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "添加数据同步", exact: true }).click();
  await page.getByLabel("访问方式", { exact: true }).click();
  await page
    .getByRole("option", {
      name: "SFTP · 普通文件采集（源端无需安装软件）",
      exact: true,
    })
    .click();
  await expect(page.getByText(/平台自动使用 rclone/)).toBeVisible();
  await page.getByLabel("源主机地址", { exact: true }).fill("192.0.2.15");
  await page.getByLabel("登录用户名", { exact: true }).fill("backup");
  await page.getByLabel("登录密码", { exact: true }).fill("protected-password");
  await page.getByLabel("源目录", { exact: true }).fill("/data");
  await page
    .getByRole("button", { name: "测试连接与浏览目录", exact: true })
    .click();
  await expect(
    page.getByText("SHA256:test-fingerprint", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "信任此指纹并测试连接", exact: true })
    .click();
  await expect(page.getByText("已连接 · /data", { exact: true })).toBeVisible();
  await page.getByLabel("备份到仓库", { exact: true }).click();
  await page.getByRole("option", { name: "sync-repo", exact: true }).click();
  await page.getByRole("button", { name: "保存", exact: true }).click();
  expect(writes.at(-1).path).toBe("/sync/tasks");
  expect(writes.at(-1).body).toMatchObject({
    access_method: "sftp",
    interval_seconds: 60,
    max_delete_percent: 20,
    repository_id: 100,
    fingerprint: "SHA256:test-fingerprint",
    source_path: "/data",
    password: "protected-password",
  });
  await expect(
    page.getByRole("dialog", { name: "添加数据同步", exact: true }),
  ).toHaveCount(0);
});

test("platform replication uses system pairing and a repository-only task form", async ({
  page,
}) => {
  const writes: { path: string; body: any }[] = [];
  const repos = [
    { repository: { id: 1, name: "business", kind: "filesystem" } },
    {
      repository: {
        id: 2,
        name: "replica-abc",
        display_name: "【异地复制】old",
        kind: "filesystem",
        replica: true,
      },
    },
  ];
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api", "");
    if (path === "/events") {
      await route.abort();
      return;
    }
    if (route.request().method() !== "GET") {
      writes.push({ path, body: route.request().postDataJSON() });
      await route.fulfill({
        json: {
          id: 42,
          code: "one-time-pair-code",
          expires_at: "2026-10-16T12:00:00Z",
        },
      });
      return;
    }
    const data: Record<string, any> = {
      "/auth/me": {
        user: { id: 1, username: "admin", role: "admin", must_change: false },
        csrf: "csrf",
      },
      "/repositories": repos,
      "/peers": [
        {
          id: 1,
          name: "灾备平台",
          direction: "outbound",
          url: "https://192.0.2.2",
        },
      ],
      "/replications": [],
      "/system": { network: "[]", disks: [] },
      "/overview": {},
      "/connections": {},
    };
    await route.fulfill({ json: data[path] ?? [] });
  });
  await page.goto("/#replication");
  await expect(page.getByText("目标平台：灾备平台")).toBeVisible();
  await page.getByRole("button", { name: "新建任务", exact: true }).click();
  const modal = page
    .locator(".arco-modal")
    .filter({ hasText: "新建异地复制任务" });
  await modal.getByLabel("需要备份的仓库").click();
  await page.getByRole("option", { name: "business", exact: true }).click();
  await expect(modal.getByLabel("目标仓库名称")).toHaveValue(
    "【异地复制】business",
  );
  await expect(modal.getByText("独立调度")).toHaveCount(0);
  await expect(modal.getByText("SSH 用户")).toHaveCount(0);
  await modal.getByLabel("目标仓库名称").fill("公司数据灾备");
  await modal.getByRole("button", { name: "确定", exact: true }).click();
  await expect
    .poll(() => writes.find((w) => w.path === "/replications")?.body)
    .toEqual({ repository_id: 1, remote_name: "公司数据灾备" });
  await page
    .locator("nav")
    .getByRole("link", { name: "系统管理", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "异地复制平台互信" }),
  ).toBeVisible();
  const trustSection = page.locator("section").filter({
    has: page.getByRole("heading", { name: "异地复制平台互信", exact: true }),
  });
  const notificationSection = page.locator("section").filter({
    has: page.getByRole("heading", {
      name: "告警通知与配置备份",
      exact: true,
    }),
  });
  const trustBox = await trustSection.boundingBox();
  const notificationBox = await notificationSection.boundingBox();
  expect(trustBox).not.toBeNull();
  expect(notificationBox).not.toBeNull();
  expect(notificationBox!.y).toBeGreaterThanOrEqual(
    trustBox!.y + trustBox!.height,
  );
  await expect(trustSection.locator("section")).toHaveCount(0);
  await page
    .getByRole("button", { name: "生成接收配对码", exact: true })
    .click();
  await expect(page.getByText(/one-time-pair-code/)).toBeVisible();
});

test("replication rows expose only the available start or pause control", async ({
  page,
}) => {
  const tasks = [
    {
      id: 11,
      peer_id: 1,
      repository_id: 1,
      remote_name: "running-target",
      enabled: true,
      state: "ready",
      active: false,
    },
    {
      id: 12,
      peer_id: 1,
      repository_id: 1,
      remote_name: "paused-target",
      enabled: false,
      state: "paused",
      active: false,
    },
  ];
  const writes: any[] = [];
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api", "");
    if (path === "/events") {
      await route.abort();
      return;
    }
    if (route.request().method() !== "GET") {
      const body = route.request().postDataJSON();
      writes.push({ path, body });
      const task = tasks.find((t) => t.id === body.id);
      if (task) {
        task.enabled = body.enabled;
        task.state = body.enabled ? "pending" : "paused";
      }
      await route.fulfill({ json: { id: body.id, enabled: body.enabled } });
      return;
    }
    const data: Record<string, any> = {
      "/auth/me": {
        user: { id: 1, username: "admin", role: "admin", must_change: false },
        csrf: "csrf",
      },
      "/repositories": [
        { repository: { id: 1, name: "source", kind: "filesystem" } },
      ],
      "/replications": tasks,
      "/peers": [{ id: 1, direction: "outbound", url: "https://192.0.2.2" }],
      "/overview": {},
    };
    await route.fulfill({ json: data[path] ?? [] });
  });
  await page.goto("/#replication");
  const running = page.getByRole("row").filter({ hasText: "running-target" });
  const paused = page.getByRole("row").filter({ hasText: "paused-target" });
  await expect(
    running.getByRole("button", { name: "暂停", exact: true }),
  ).toBeVisible();
  await expect(
    running.getByRole("button", { name: "开始", exact: true }),
  ).toHaveCount(0);
  await expect(
    paused.getByRole("button", { name: "开始", exact: true }),
  ).toBeVisible();
  await expect(
    paused.getByRole("button", { name: "暂停", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "立即复制", exact: true }),
  ).toHaveCount(0);
  await running.getByRole("button", { name: "暂停", exact: true }).click();
  await expect(
    running.getByRole("button", { name: "开始", exact: true }),
  ).toBeVisible();
  await running.getByRole("button", { name: "开始", exact: true }).click();
  await expect(
    running.getByRole("button", { name: "暂停", exact: true }),
  ).toBeVisible();
  expect(writes).toEqual([
    { path: "/replications", body: { id: 11, enabled: false } },
    { path: "/replications", body: { id: 11, enabled: true } },
  ]);
});
