import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";

test("HTTPS login, all workspaces, real UI repository creation and snapshot", async ({
  page,
}) => {
  const passwordFile = process.env.BC_TEST_PASSWORD_FILE;
  test.skip(
    !passwordFile,
    "Set BC_TEST_PASSWORD_FILE to a protected admin password file",
  );
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/");
  await page
    .getByLabel("用户名", { exact: true })
    .fill(process.env.BC_TEST_USER || "admin");
  await page
    .getByLabel("密码", { exact: true })
    .fill(readFileSync(passwordFile!, "utf8").trim());
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "概览", exact: true }),
  ).toBeVisible();
  for (const label of [
    "备份仓库",
    "数据同步",
    "快照",
    "共享访问",
    "快照策略",
    "异地复制",
    "监控告警",
    "任务中心",
    "系统管理",
    "概览",
  ]) {
    await page
      .locator("nav")
      .getByRole("link", { name: label, exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: label, exact: true }),
    ).toBeVisible();
  }
  await page.screenshot({
    path: "/tmp/backup-center-overview.png",
    fullPage: true,
  });
  if (process.env.BC_TEST_MUTATION === "1") {
    const name = "ui-verify-" + Date.now();
    await page
      .locator("nav")
      .getByRole("link", { name: "备份仓库", exact: true })
      .click();
    await page.getByRole("button", { name: "创建仓库", exact: true }).click();
    await page.getByLabel("仓库名称", { exact: true }).fill(name);
    await page.getByLabel("容量配额", { exact: true }).fill("1G");
    await page.getByRole("button", { name: "创建", exact: true }).click();
    await page.getByLabel("筛选仓库", { exact: true }).fill(name);
    await expect(page.getByText(name, { exact: true }).first()).toBeVisible({
      timeout: 30000,
    });
    const row = page
      .locator("tr")
      .filter({ has: page.getByText(name, { exact: true }) });
    await row.getByRole("button", { name: "创建快照", exact: true }).click();
    await page
      .locator("nav")
      .getByRole("link", { name: "快照", exact: true })
      .click();
    await page.getByLabel("筛选快照", { exact: true }).fill(name);
    await expect(page.getByText(new RegExp(name + "@bc-")).first()).toBeVisible(
      { timeout: 30000 },
    );
    // Clean up only the specifically created UI verification repository, through authenticated APIs.
    await page.evaluate(async (repoName) => {
      const me = await fetch("/api/auth/me").then((r) => r.json());
      const repos = await fetch("/api/repositories").then((r) => r.json());
      const repo = repos.find(
        (r: any) => r.repository.name === repoName,
      ).repository;
      const post = async (kind: string, body: any) => {
        const response = await fetch("/api/actions/" + kind, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "X-CSRF-Token": me.csrf,
          },
          body: JSON.stringify(body),
        });
        const job = await response.json();
        if (!response.ok) throw new Error(job.error);
        for (let i = 0; i < 120; i++) {
          const jobs = await fetch("/api/jobs").then((r) => r.json());
          const current = jobs.find((j: any) => j.id === job.id);
          if (current.status === "succeeded") return;
          if (current.status === "failed") throw new Error(current.error);
          await new Promise((resolve) => setTimeout(resolve, 250));
        }
        throw new Error("cleanup task timed out");
      };
      const snaps = await fetch(
        "/api/repositories/" + repo.id + "/snapshots",
      ).then((r) => r.json());
      for (const snapshot of snaps)
        await post("snapshot.delete", {
          repository_id: repo.id,
          snapshot: snapshot.name,
          confirm: snapshot.name,
        });
      await post("repository.delete", {
        repository_id: repo.id,
        confirm: repo.name,
      });
    }, name);
  }
  expect(errors).toEqual([]);
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "登录备份中心" }),
  ).toBeVisible();
});
