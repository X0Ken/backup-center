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
export default function Jobs({ rev }: { rev: number }) {
  return (
    <>
      <DataTable
        rows={null}
        endpoint="/jobs"
        revision={rev}
        tableName="任务"
        columns={[
          { title: "#", dataIndex: "id", width: 70 },
          {
            title: "操作",
            dataIndex: "kind",
            render: (v: string) => kindLabels[v] || v,
          },
          ...jobColumns.slice(1),
        ]}
      />
      <Alert
        style={{ marginTop: 20 }}
        content="任务持久化保存在本机。服务重启后，排队任务继续执行，执行中断的任务标记为中断；请核对实际资源后重新提交。"
      />
    </>
  );
}
