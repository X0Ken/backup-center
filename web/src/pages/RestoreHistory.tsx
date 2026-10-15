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
export default function RestoreHistory({ rev }: { rev: number }) {
  const d = useData<Row[]>("/jobs", rev);
  return (
    <section>
      <h2>恢复记录</h2>
      <DataTable
        rows={d.data?.filter((j) => j.kind.startsWith("restore.")) || []}
        columns={jobColumns}
      />
    </section>
  );
}
