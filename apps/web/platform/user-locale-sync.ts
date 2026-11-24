"use client";

import { usePathname } from "next/navigation";

export function useUserLocaleSyncEnabled(): boolean {
  const pathname = usePathname();
  // Wait until the router has a pathname. There is no auth callback to pause for.
  return !!pathname;
}
