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
        version: "1.1.0",
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
  await expect(page.locator(".page-heading p")).toContainText("中断续传");
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
