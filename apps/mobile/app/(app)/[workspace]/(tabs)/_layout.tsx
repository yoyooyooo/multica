import { useState } from "react";
import { Tabs } from "expo-router";
import { Ionicons } from "@expo/vector-icons";
import { GlobalNavMenu } from "@/components/nav/global-nav-menu";

const ACTIVE = "#2e2e33"; // matches tailwind.config.js primary
const INACTIVE = "#71717a"; // matches muted-foreground

export default function TabsLayout() {
  // The "More" tab doesn't navigate to a screen — its tabPress is
  // intercepted to open the global nav popover. State is lifted here so
  // the listener and the Modal share the same boolean. The stub
  // more.tsx file exists only because expo-router requires a route
  // entry to register a Tabs.Screen.
  const [menuOpen, setMenuOpen] = useState(false);

  return (
    <>
      <Tabs
        screenOptions={{
          headerShown: false,
          tabBarActiveTintColor: ACTIVE,
          tabBarInactiveTintColor: INACTIVE,
          tabBarLabelStyle: { fontSize: 11 },
        }}
      >
        <Tabs.Screen
          name="inbox"
          options={{
            title: "Inbox",
            tabBarIcon: ({ color, size, focused }) => (
              <Ionicons
                name={focused ? "mail" : "mail-outline"}
                size={size}
                color={color}
              />
            ),
          }}
        />
        <Tabs.Screen
          name="my-issues"
          options={{
            title: "My Issues",
            tabBarIcon: ({ color, size, focused }) => (
              <Ionicons
                name={focused ? "list" : "list-outline"}
                size={size}
                color={color}
              />
            ),
          }}
        />
        <Tabs.Screen
          name="chat"
          options={{
            title: "Chat",
            tabBarIcon: ({ color, size, focused }) => (
              <Ionicons
                name={focused ? "chatbubble" : "chatbubble-outline"}
                size={size}
                color={color}
              />
            ),
          }}
        />
        <Tabs.Screen
          name="more"
          options={{
            title: "More",
            tabBarIcon: ({ color, size }) => (
              <Ionicons
                name={menuOpen ? "menu" : "menu-outline"}
                size={size}
                color={color}
              />
            ),
          }}
          listeners={() => ({
            tabPress: (e) => {
              // Open the popover instead of navigating into the stub
              // more.tsx route. Without preventDefault expo-router
              // would push that route and briefly mount the stub.
              e.preventDefault();
              setMenuOpen(true);
            },
          })}
        />
      </Tabs>
      <GlobalNavMenu visible={menuOpen} onClose={() => setMenuOpen(false)} />
    </>
  );
}
