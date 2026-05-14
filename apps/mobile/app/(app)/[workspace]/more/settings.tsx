/**
 * Settings page — account info, workspace switching, and sign out.
 *
 * Inherits the responsibilities the old More tab carried (account row,
 * workspace list, sign-out button) now that the More tab is gone and global
 * navigation lives in GlobalNavMenu.
 */
import { ActivityIndicator, ScrollView, Pressable, View } from "react-native";
import { Ionicons } from "@expo/vector-icons";
import { router } from "expo-router";
import { useQuery } from "@tanstack/react-query";
import type { Workspace } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { Button } from "@/components/ui/button";
import { workspaceListOptions } from "@/data/queries/workspaces";
import { useAuthStore } from "@/data/auth-store";
import { useWorkspaceStore } from "@/data/workspace-store";
import { cn } from "@/lib/utils";

export default function SettingsPage() {
  const user = useAuthStore((s) => s.user);
  const logout = useAuthStore((s) => s.logout);
  const currentSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const setCurrentWorkspace = useWorkspaceStore((s) => s.setCurrentWorkspace);
  const clearWorkspace = useWorkspaceStore((s) => s.clear);
  const { data, isLoading, error } = useQuery(workspaceListOptions());

  const onSwitch = async (ws: Workspace) => {
    if (ws.slug === currentSlug) return;
    await setCurrentWorkspace(ws.id, ws.slug);
    router.replace(`/${ws.slug}/inbox`);
  };

  const onSignOut = async () => {
    await clearWorkspace();
    await logout();
  };

  return (
    <ScrollView
      className="flex-1 bg-background"
      contentContainerClassName="px-4 py-4 gap-6"
    >
      <SectionGroup title="Account">
        <View className="p-4">
          <Text className="text-base font-medium text-foreground">
            {user?.name ?? "—"}
          </Text>
          <Text className="text-sm text-muted-foreground mt-1">
            {user?.email}
          </Text>
        </View>
      </SectionGroup>

      <SectionGroup title="Workspaces">
        {isLoading ? (
          <View className="py-4 items-center">
            <ActivityIndicator />
          </View>
        ) : error ? (
          <View className="p-4">
            <Text className="text-sm text-destructive">
              Failed to load workspaces
            </Text>
          </View>
        ) : (
          data?.map((ws, idx) => {
            const isActive = ws.slug === currentSlug;
            const isLast = idx === (data?.length ?? 0) - 1;
            return (
              <WorkspaceRow
                key={ws.id}
                name={ws.name}
                slug={ws.slug}
                isActive={isActive}
                isLast={isLast}
                onPress={() => onSwitch(ws)}
              />
            );
          })
        )}
      </SectionGroup>

      <View className="pt-2">
        <Button variant="outline" onPress={onSignOut}>
          Sign out
        </Button>
      </View>
    </ScrollView>
  );
}

function SectionGroup({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <View className="gap-2">
      <Text className="text-xs uppercase tracking-wider text-muted-foreground">
        {title}
      </Text>
      <View className="rounded-md border border-border bg-card overflow-hidden">
        {children}
      </View>
    </View>
  );
}

function WorkspaceRow({
  name,
  slug,
  isActive,
  isLast,
  onPress,
}: {
  name: string;
  slug: string;
  isActive: boolean;
  isLast: boolean;
  onPress: () => void;
}) {
  return (
    <Pressable
      onPress={onPress}
      disabled={isActive}
      className={cn(
        "flex-row items-center px-4 py-3.5 active:bg-secondary",
        !isLast && "border-b border-border",
      )}
    >
      <View className="flex-1">
        <Text className="text-base font-medium text-foreground">{name}</Text>
        <Text className="text-xs text-muted-foreground mt-0.5">/{slug}</Text>
      </View>
      {isActive ? (
        <Ionicons name="checkmark" size={18} color="#71717a" />
      ) : (
        <Ionicons name="chevron-forward" size={18} color="#71717a" />
      )}
    </Pressable>
  );
}
