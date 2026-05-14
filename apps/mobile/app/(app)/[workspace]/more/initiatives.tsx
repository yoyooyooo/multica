import { View } from "react-native";
import { Text } from "@/components/ui/text";

/**
 * Initiatives placeholder. Read-only list of workspace initiatives, filled
 * in a later phase to mirror the web Initiatives surface.
 */
export default function InitiativesPage() {
  return (
    <View className="flex-1 items-center justify-center bg-background px-6">
      <Text className="text-sm text-muted-foreground text-center">
        Initiatives coming soon.
      </Text>
    </View>
  );
}
