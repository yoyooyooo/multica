import { View } from "react-native";
import { Text } from "@/components/ui/text";

/**
 * Teams placeholder. Workspace team list, filled in a later phase to mirror
 * the web Teams surface.
 */
export default function TeamsPage() {
  return (
    <View className="flex-1 items-center justify-center bg-background px-6">
      <Text className="text-sm text-muted-foreground text-center">
        Teams coming soon.
      </Text>
    </View>
  );
}
