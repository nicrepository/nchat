import { useContext } from "react";

import { AdminSessionContext, type AdminSessionValue } from "./AdminSessionContext";

export function useAdminSession(): AdminSessionValue {
  const value = useContext(AdminSessionContext);
  if (value === null) {
    throw new Error("useAdminSession must be used inside AdminSessionProvider");
  }
  return value;
}
