import { useEffect, useState } from "react";
export function currentPage() {
  const page = location.hash.slice(1).split("?")[0];
  return page === "restore" ? "snapshots" : page || "overview";
}
function repositoryFromHash() {
  const raw = new URLSearchParams(location.hash.split("?")[1] || "").get(
    "repository_id",
  );
  const id = Number(raw);
  return Number.isSafeInteger(id) && id > 0 ? id : undefined;
}
export function useRepositoryFilter(): [
  number | undefined,
  (id: number | undefined) => void,
] {
  const [id, setID] = useState(repositoryFromHash);
  useEffect(() => {
    const update = () => setID(repositoryFromHash());
    window.addEventListener("hashchange", update);
    return () => window.removeEventListener("hashchange", update);
  }, []);
  return [
    id,
    (value) => {
      setID(value);
      history.replaceState(
        null,
        "",
        `#${currentPage()}${value ? `?repository_id=${value}` : ""}`,
      );
    },
  ];
}
