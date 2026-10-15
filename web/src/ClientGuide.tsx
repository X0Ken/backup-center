import React, { useState } from "react";
import { Alert, Button, Message, Modal, Tabs } from "@arco-design/web-react";
import { api } from "./api";
function Content({ data }: { data: any }) {
  const [selected, setSelected] = useState("mount");
  const entries: { key: string; title: string; script: string }[] = [
    { key: "mount", title: "连接与挂载", script: data.script },
    ...Object.entries(data.scripts || {}).map(([tool, script]) => ({
      key: tool,
      title:
        (
          {
            rsync: "文件备份",
            xtrabackup: "MySQL 备份",
            pg_basebackup: "PostgreSQL 备份",
          } as Record<string, string>
        )[tool] || tool,
      script: String(script),
    })),
  ];
  const script = entries.find((e) => e.key === selected)!.script;
  return (
    <>
      <Alert content="先完成连接，再选择与业务匹配的源端备份脚本。数据库必须使用匹配版本的专用备份工具。" />
      <Tabs activeTab={selected} onChange={setSelected}>
        {entries.map((e) => (
          <Tabs.TabPane key={e.key} title={e.title} />
        ))}
      </Tabs>
      <pre className="code-block">{script}</pre>
      <Button
        type="primary"
        onClick={() =>
          navigator.clipboard
            .writeText(script)
            .then(() => Message.success("当前脚本已复制"))
            .catch(() => Message.error("复制失败，请手动复制"))
        }
      >
        复制当前脚本
      </Button>
    </>
  );
}
export async function showClientGuide(id: number) {
  try {
    const data = await api("/client-guide/" + id);
    Modal.info({
      title: "源服务器接入与备份指南",
      style: { width: 960 },
      content: <Content data={data} />,
      okText: "关闭",
    });
  } catch (e: any) {
    Message.error(e.message);
  }
}
