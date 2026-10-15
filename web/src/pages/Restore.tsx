import React from "react";
import { Alert, Button, Tag } from "@arco-design/web-react";
import { IconHistory } from "@arco-design/web-react/icon";
import { bytes, date } from "../api";
import { DataTable, Row } from "../components";
import { Tools } from "../types";
import RestoreHistory from "./RestoreHistory";
export default function Restore({
  snapshots,
  repos,
  admin,
  form,
  perform,
  rev,
  browse,
}: Pick<Tools, "repos" | "admin" | "form" | "perform" | "rev"> & {
  snapshots: Row[] | null;
  browse: (s: Row) => void;
}) {
  return (
    <>
      <div className="restore-banner">
        <span className="restore-symbol">
          <IconHistory />
        </span>
        <div>
          <h2>选择一个恢复时间点，安全地找回数据</h2>
          <p>优先使用克隆恢复：新仓库独立写入，源仓库继续保持现状。</p>
        </div>
        <Tag color="green">保留原始备份</Tag>
      </div>
      <Alert
        type="warning"
        content="恢复数据库前，请确认源端备份已经完成一致性检查。原地回滚仅支持最新快照，必须先关闭协议导出并停止客户端写入。"
      />
      <section>
        <div className="section-title">
          <h2>可恢复快照</h2>
          <span className="muted">选择克隆、文件下载或原地回滚</span>
        </div>
        <DataTable
          rows={snapshots}
          rowKey="name"
          columns={[
            { title: "恢复点", dataIndex: "name" },
            {
              title: "创建时间",
              dataIndex: "created_at",
              render: date,
            },
            {
              title: "引用数据量",
              dataIndex: "referenced",
              render: bytes,
            },
            {
              title: "恢复方式",
              render: (_: any, s: Row) => (
                <div className="row-actions">
                  <Button
                    type="text"
                    onClick={() =>
                      form(
                        "克隆为新仓库",
                        [
                          {
                            key: "name",
                            label: "新仓库名称",
                            required: true,
                          },
                        ],
                        (v) =>
                          perform("restore.clone", {
                            repository_id: s.repository_id,
                            snapshot: s.name,
                            ...v,
                          }),
                      )
                    }
                  >
                    克隆恢复
                  </Button>
                  <Button type="text" onClick={() => browse(s)}>
                    浏览文件
                  </Button>
                  {admin && (
                    <Button
                      type="text"
                      status="danger"
                      onClick={() => {
                        const r = repos?.find(
                          (x) => x.repository.id === s.repository_id,
                        )?.repository;
                        form(
                          "原地回滚",
                          [
                            {
                              key: "confirm",
                              label: `输入仓库名称 ${r?.name}`,
                              required: true,
                            },
                            {
                              key: "acknowledge_offline",
                              label: "已停止客户端写入并关闭导出",
                              type: "switch",
                            },
                          ],
                          (v) =>
                            perform("restore.rollback", {
                              repository_id: s.repository_id,
                              snapshot: s.name,
                              ...v,
                            }),
                          "此操作丢弃快照之后未保存的更改。存在更新快照时会拒绝回滚，请改用克隆恢复。",
                        );
                      }}
                    >
                      回滚
                    </Button>
                  )}
                </div>
              ),
            },
          ]}
        />
      </section>
      <RestoreHistory rev={rev} />
    </>
  );
}
