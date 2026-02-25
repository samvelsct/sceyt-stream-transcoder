#!/usr/bin/env python3
"""
StreamBridge Python Client Example

This example demonstrates how to control StreamBridge from Python.
"""

import grpc
import sys
import time
import argparse
from typing import Optional

# Import generated protobuf code
# Note: You need to generate Python protobuf files first
# Run: python -m grpc_tools.protoc -I../../proto --python_out=. --grpc_python_out=. ../../proto/streambridge.proto
try:
    import streambridge_pb2
    import streambridge_pb2_grpc
except ImportError:
    print("Error: Generated protobuf files not found.")
    print("Run: python -m grpc_tools.protoc -I../../proto --python_out=. --grpc_python_out=. ../../proto/streambridge.proto")
    sys.exit(1)


class StreamBridgeClient:
    """Client for StreamBridge gRPC service"""

    def __init__(self, server_address: str = "localhost:50051"):
        """Initialize client and connect to server"""
        self.server_address = server_address
        self.channel = grpc.insecure_channel(server_address)
        self.stub = streambridge_pb2_grpc.StreamBridgeStub(self.channel)
        print(f"Connected to StreamBridge at {server_address}")

    def create_session(
        self,
        session_id: str,
        output_path: str,
        enable_gst: bool = False
    ) -> bool:
        """Create a new HLS session"""
        request = streambridge_pb2.CreateSessionRequest(
            session_id=session_id,
            output_path=output_path,
            enable_gst=enable_gst
        )
        response = self.stub.CreateSession(request)
        print(f"✓ {response.message}")
        return response.success

    def destroy_session(self, session_id: str) -> bool:
        """Destroy an existing session"""
        request = streambridge_pb2.DestroySessionRequest(session_id=session_id)
        response = self.stub.DestroySession(request)
        print(f"✓ {response.message}")
        return response.success

    def add_input(
        self,
        session_id: str,
        janus_room_id: int,
        janus_session_id: int,
        janus_handle_id: int,
        janus_publisher_id: int,
        display_name: str,
        janus_gateway_address: str = "",
        janus_admin_key: str = "",
        janus_admin_secret: str = ""
    ) -> bool:
        """Add a WebRTC input to the session"""
        request = streambridge_pb2.AddInputRequest(
            session_id=session_id,
            janus_room_id=janus_room_id,
            janus_session_id=janus_session_id,
            janus_handle_id=janus_handle_id,
            janus_publisher_id=janus_publisher_id,
            display_name=display_name,
            janus_gateway_address=janus_gateway_address,
            janus_admin_key=janus_admin_key,
            janus_admin_secret=janus_admin_secret
        )
        response = self.stub.AddInput(request)
        print(f"✓ {response.message}")
        return response.success

    def remove_input(
        self,
        session_id: str,
        janus_session_id: int,
        janus_handle_id: int,
        display_name: str
    ) -> bool:
        """Remove a WebRTC input from the session"""
        request = streambridge_pb2.RemoveInputRequest(
            session_id=session_id,
            janus_session_id=janus_session_id,
            janus_handle_id=janus_handle_id,
            display_name=display_name
        )
        response = self.stub.RemoveInput(request)
        print(f"✓ {response.message}")
        return response.success

    def set_mute(
        self,
        session_id: str,
        user_id: str,
        client_id: str,
        mute: bool
    ) -> bool:
        """Set mute status for a participant"""
        request = streambridge_pb2.SetMuteRequest(
            session_id=session_id,
            user_id=user_id,
            client_id=client_id,
            mute=mute
        )
        response = self.stub.SetMute(request)
        print(f"✓ {response.message}")
        return response.success

    def set_video_on(
        self,
        session_id: str,
        user_id: str,
        client_id: str,
        video_on: bool
    ) -> bool:
        """Set video on/off status for a participant"""
        request = streambridge_pb2.SetVideoOnRequest(
            session_id=session_id,
            user_id=user_id,
            client_id=client_id,
            video_on=video_on
        )
        response = self.stub.SetVideoOn(request)
        print(f"✓ {response.message}")
        return response.success

    def write_id3_tag(
        self,
        session_id: str,
        event_data: str,
        event_type: str
    ) -> bool:
        """Write custom ID3 tag to HLS stream"""
        request = streambridge_pb2.WriteID3TagRequest(
            session_id=session_id,
            event_data=event_data,
            event_type=event_type
        )
        response = self.stub.WriteID3Tag(request)
        print(f"✓ {response.message}")
        return response.success

    def get_session_info(self, session_id: str) -> Optional[dict]:
        """Get session information"""
        request = streambridge_pb2.GetSessionInfoRequest(session_id=session_id)
        response = self.stub.GetSessionInfo(request)
        if response.success:
            return {
                "participant_count": response.participant_count,
                "participant_names": list(response.participant_names)
            }
        return None

    def close(self):
        """Close the gRPC channel"""
        self.channel.close()


def run_demo(server_address: str):
    """Run a complete demo of StreamBridge functionality"""
    client = StreamBridgeClient(server_address)

    try:
        # Create session
        print("\n=== Creating Session ===")
        client.create_session(
            session_id="python-demo-123",
            output_path="python-demo/stream.m3u8",
            enable_gst=False
        )

        # Add first participant
        print("\n=== Adding First Participant ===")
        client.add_input(
            session_id="python-demo-123",
            janus_room_id=1234,
            janus_session_id=5678,
            janus_handle_id=9012,
            janus_publisher_id=3456,
            display_name="Alice"
        )

        # Add second participant
        print("\n=== Adding Second Participant ===")
        client.add_input(
            session_id="python-demo-123",
            janus_room_id=1234,
            janus_session_id=5679,
            janus_handle_id=9013,
            janus_publisher_id=3457,
            display_name="Bob"
        )

        # Get session info
        print("\n=== Getting Session Info ===")
        info = client.get_session_info("python-demo-123")
        if info:
            print(f"✓ Participants: {info['participant_count']}")
            for i, name in enumerate(info['participant_names'], 1):
                print(f"  {i}. {name}")

        # Set mute status
        print("\n=== Setting Mute Status ===")
        client.set_mute(
            session_id="python-demo-123",
            user_id="user-alice",
            client_id="client-001",
            mute=True
        )

        # Set video status
        print("\n=== Setting Video Status ===")
        client.set_video_on(
            session_id="python-demo-123",
            user_id="user-bob",
            client_id="client-002",
            video_on=False
        )

        # Write custom ID3 tag
        print("\n=== Writing Custom ID3 Tag ===")
        client.write_id3_tag(
            session_id="python-demo-123",
            event_data='{"event":"hand-raised","userId":"user-alice","timestamp":"2024-02-19T10:30:00Z"}',
            event_type="custom-event"
        )

        # Keep session running
        print("\n=== Session Running ===")
        print("Keeping session active for 10 seconds...")
        time.sleep(10)

        # Remove participant
        print("\n=== Removing Participant ===")
        client.remove_input(
            session_id="python-demo-123",
            janus_session_id=5679,
            janus_handle_id=9013,
            display_name="Bob"
        )

        # Get updated info
        print("\n=== Getting Updated Session Info ===")
        info = client.get_session_info("python-demo-123")
        if info:
            print(f"✓ Participants: {info['participant_count']}")
            for i, name in enumerate(info['participant_names'], 1):
                print(f"  {i}. {name}")

        # Destroy session
        print("\n=== Destroying Session ===")
        client.destroy_session("python-demo-123")

        print("\n=== Demo Complete ===")
        print("All operations completed successfully!")

    except grpc.RpcError as e:
        print(f"\nError: {e.code()}: {e.details()}")
        sys.exit(1)
    finally:
        client.close()


def main():
    parser = argparse.ArgumentParser(description="StreamBridge Python Client")
    parser.add_argument(
        "--server",
        default="localhost:50051",
        help="StreamBridge server address (default: localhost:50051)"
    )
    args = parser.parse_args()

    run_demo(args.server)


if __name__ == "__main__":
    main()
